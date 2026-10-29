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

-- Keep queue keys and refresh protocol stable, skipping match updates that
-- change no reader input (including timestamp-only maintenance writes).
-- +goose StatementBegin
CREATE FUNCTION app.mark_coverage_display_dirty() RETURNS trigger
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE old_account uuid; new_account uuid;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF TG_TABLE_NAME = 'workout_segment_matches' THEN
            -- Compare exact EWKB without resolving PostGIS equality operators
            -- through this definer's deliberately restricted search_path.
            IF (OLD.account_id, OLD.workout_id, OLD.logical_path_id, OLD.region_id, OLD.generation_id,
                    OLD.physical_segment_id, OLD.first_traversed_at, OLD.first_route_order, OLD.covered_meters, public.ST_AsEWKB(OLD.geom))
                IS NOT DISTINCT FROM (NEW.account_id, NEW.workout_id, NEW.logical_path_id, NEW.region_id, NEW.generation_id,
                    NEW.physical_segment_id, NEW.first_traversed_at, NEW.first_route_order, NEW.covered_meters, public.ST_AsEWKB(NEW.geom)) THEN
                RETURN NULL;
            END IF;
        ELSIF (OLD.account_id, OLD.region_id, OLD.generation_id, OLD.physical_segment_id,
                OLD.logical_path_id, OLD.highway, OLD.tags) IS NOT DISTINCT FROM
            (NEW.account_id, NEW.region_id, NEW.generation_id, NEW.physical_segment_id,
                NEW.logical_path_id, NEW.highway, NEW.tags) THEN
            RETURN NULL;
        END IF;
    END IF;
    IF TG_OP <> 'INSERT' THEN
        IF TG_TABLE_NAME = 'workout_segment_matches' THEN
            INSERT INTO app.coverage_display_dirty(transaction_id, account_id, scope, workout_id)
            VALUES (txid_current(), OLD.account_id, 'workout', OLD.workout_id) ON CONFLICT DO NOTHING;
        ELSE
            INSERT INTO app.coverage_display_dirty(transaction_id, account_id, scope, region_id, generation_id, physical_segment_id)
            VALUES (txid_current(), OLD.account_id, 'segment', OLD.region_id, OLD.generation_id, OLD.physical_segment_id)
            ON CONFLICT DO NOTHING;
        END IF;
        old_account := OLD.account_id;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        IF TG_TABLE_NAME = 'workout_segment_matches' THEN
            INSERT INTO app.coverage_display_dirty(transaction_id, account_id, scope, workout_id)
            VALUES (txid_current(), NEW.account_id, 'workout', NEW.workout_id) ON CONFLICT DO NOTHING;
        ELSE
            INSERT INTO app.coverage_display_dirty(transaction_id, account_id, scope, region_id, generation_id, physical_segment_id)
            VALUES (txid_current(), NEW.account_id, 'segment', NEW.region_id, NEW.generation_id, NEW.physical_segment_id)
            ON CONFLICT DO NOTHING;
        END IF;
        new_account := NEW.account_id;
    END IF;
    INSERT INTO app.coverage_display_dirty(transaction_id, account_id, scope)
    SELECT DISTINCT txid_current(), account_id, 'account' FROM unnest(ARRAY[old_account, new_account]) account_id
    WHERE account_id IS NOT NULL ORDER BY account_id ON CONFLICT DO NOTHING;
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


CREATE INDEX workout_path_display_partials_entity_idx
    ON app.workout_path_display_partials (account_id, entity_id, workout_id);
CREATE INDEX workout_path_display_context_entity_idx
    ON app.workout_path_display_context (account_id, entity_id, workout_id);
CREATE INDEX workout_park_display_bounds_park_idx
    ON app.workout_park_display_bounds (account_id, park_id, workout_id);

-- Display identity is a read-model policy, not a matcher/source-data migration.
-- +goose StatementBegin
DO $block$
BEGIN
    IF current_setting('server_version_num')::integer NOT BETWEEN 180000 AND 189999
        OR current_setting('server_encoding') <> 'UTF8'
        OR NOT EXISTS (SELECT 1 FROM pg_collation WHERE oid = to_regcollation('pg_catalog.pg_c_utf8')
            AND collprovider = 'b' AND collencoding = pg_char_to_encoding('UTF8')) THEN
        RAISE EXCEPTION 'coverage-display-identity-v1 requires PostgreSQL 18 UTF8 with builtin pg_c_utf8'
            USING ERRCODE = '55000';
    END IF;
END;
$block$;
-- +goose StatementEnd

CREATE TABLE app.coverage_display_revisions (
    account_id uuid PRIMARY KEY REFERENCES app.accounts(id) ON DELETE CASCADE,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    last_invalidated_transaction_id bigint
);
CREATE TABLE app.coverage_display_contexts (
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE CASCADE,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    generation bigint NOT NULL,
    revision bigint NOT NULL,
    start_date date NOT NULL,
    end_date date NOT NULL,
    policy_version text NOT NULL DEFAULT 'coverage-display-identity-v1'
        CHECK (policy_version = 'coverage-display-identity-v1'),
    label_locale text NOT NULL DEFAULT 'en' CHECK (label_locale = 'en'),
    ready boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    last_used_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, id),
    CONSTRAINT coverage_display_contexts_scope_key
        UNIQUE (account_id, generation, revision, start_date, end_date, policy_version, label_locale),
    CHECK (start_date <= end_date)
);
CREATE INDEX coverage_display_contexts_gc_idx ON app.coverage_display_contexts(account_id, last_used_at, id);
CREATE TABLE app.coverage_display_members (
    account_id uuid NOT NULL,
    context_id uuid NOT NULL,
    source_kind text NOT NULL CHECK (source_kind IN ('path', 'park')),
    source_id uuid NOT NULL,
    canonical_id uuid NOT NULL,
    broad_class text,
    name text,
    locality_name text,
    region_id text,
    region_name text,
    key_region text COLLATE "C" NOT NULL,
    normalized_name text COLLATE "C" NOT NULL,
    normalized_locality text COLLATE "C" NOT NULL,
    key_discriminator text COLLATE "C" NOT NULL,
    range_minimum_longitude double precision,
    range_minimum_latitude double precision,
    range_maximum_longitude double precision,
    range_maximum_latitude double precision,
    PRIMARY KEY (account_id, context_id, source_kind, source_id),
    FOREIGN KEY (account_id, context_id) REFERENCES app.coverage_display_contexts(account_id, id) ON DELETE CASCADE
);
CREATE INDEX coverage_display_members_canonical_idx
    ON app.coverage_display_members(account_id, context_id, canonical_id, source_kind, source_id);
CREATE INDEX coverage_display_members_collision_idx
    ON app.coverage_display_members(account_id, canonical_id);
CREATE TABLE app.coverage_display_visits (
    account_id uuid NOT NULL,
    context_id uuid NOT NULL,
    canonical_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    local_start_date date NOT NULL,
    started_at timestamptz NOT NULL,
    minimum_longitude double precision,
    minimum_latitude double precision,
    maximum_longitude double precision,
    maximum_latitude double precision,
    PRIMARY KEY (account_id, context_id, canonical_id, workout_id),
    FOREIGN KEY (account_id, context_id) REFERENCES app.coverage_display_contexts(account_id, id) ON DELETE CASCADE
);
CREATE INDEX coverage_display_visits_date_idx
    ON app.coverage_display_visits(account_id, context_id, canonical_id, local_start_date, started_at, workout_id);
CREATE TABLE app.coverage_display_entities (
    account_id uuid NOT NULL,
    context_id uuid NOT NULL,
    canonical_id uuid NOT NULL,
    entity_id uuid GENERATED ALWAYS AS (canonical_id) STORED,
    entity_kind text NOT NULL CHECK (entity_kind IN ('path', 'park')),
    broad_class text,
    name text,
    locality_name text,
    region_id text,
    region_name text,
    range_workout_count bigint NOT NULL,
    range_first_date date,
    range_first_workout_id uuid,
    range_latest_date date,
    range_latest_workout_id uuid,
    all_time_workout_count bigint NOT NULL,
    all_time_first_date date,
    all_time_first_workout_id uuid,
    all_time_latest_date date,
    all_time_latest_workout_id uuid,
    minimum_longitude double precision,
    minimum_latitude double precision,
    maximum_longitude double precision,
    maximum_latitude double precision,
    PRIMARY KEY (account_id, context_id, canonical_id),
    FOREIGN KEY (account_id, context_id) REFERENCES app.coverage_display_contexts(account_id, id) ON DELETE CASCADE,
    CHECK (range_workout_count >= 0 AND all_time_workout_count >= range_workout_count)
);
CREATE INDEX coverage_display_entities_visible_idx ON app.coverage_display_entities(account_id, context_id, canonical_id)
    WHERE range_workout_count > 0;
-- Tombstones deliberately have no source/account FK, like the precomputation queue.
CREATE TABLE app.coverage_display_invalidations (
    txid bigint NOT NULL,
    account_id uuid NOT NULL,
    PRIMARY KEY (txid, account_id)
);

ALTER TABLE app.map_selections ADD COLUMN coverage_display_context_id uuid;
ALTER TABLE app.map_selections ADD CONSTRAINT map_selections_coverage_display_context_fk
    FOREIGN KEY (account_id, coverage_display_context_id)
    REFERENCES app.coverage_display_contexts(account_id, id)
    ON DELETE SET NULL (coverage_display_context_id);
CREATE INDEX map_selections_coverage_display_context_idx
    ON app.map_selections(account_id, coverage_display_context_id, expires_at);

ALTER TABLE app.coverage_display_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_display_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_display_revisions_owner_policy ON app.coverage_display_revisions
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.coverage_display_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_display_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_display_contexts_owner_policy ON app.coverage_display_contexts
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.coverage_display_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_display_members FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_display_members_owner_policy ON app.coverage_display_members
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.coverage_display_visits ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_display_visits FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_display_visits_owner_policy ON app.coverage_display_visits
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.coverage_display_entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_display_entities FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_display_entities_owner_policy ON app.coverage_display_entities
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.coverage_display_invalidations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_display_invalidations FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_display_invalidations_owner_policy ON app.coverage_display_invalidations
    TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);

-- Unicode White_Space is explicit; neither database locale nor regex character
-- classes can change policy v1. Direction, punctuation and accents survive.
-- +goose StatementBegin
CREATE FUNCTION app.coverage_display_normalize(value text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path = pg_catalog AS $function$
    SELECT btrim(regexp_replace(normalize(casefold(normalize(value, NFC) COLLATE pg_catalog.pg_c_utf8), NFC),
        U&'[\0009-\000D\0020\0085\00A0\1680\2000-\200A\2028\2029\202F\205F\3000]+', ' ', 'g'), ' ')
$function$;
-- +goose StatementEnd

-- Every UTF-8 tuple component has a binary network-order octet-length prefix.
-- The digest is truncated to 128 bits, then UUIDv8 and RFC variant bits are set.
-- +goose StatementBegin
CREATE FUNCTION app.coverage_display_identity(key_region text, key_name text, key_locality text, discriminator text)
RETURNS uuid LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE SET search_path = pg_catalog AS $function$
DECLARE payload bytea := ''::bytea; component text; encoded bytea; digest bytea;
BEGIN
    FOREACH component IN ARRAY ARRAY['workouts-explorer/coverage-display', 'coverage-display-identity-v1',
        key_region, key_name, key_locality, discriminator] LOOP
        encoded := convert_to(component, 'UTF8');
        payload := payload || int4send(octet_length(encoded)) || encoded;
    END LOOP;
    digest := substring(sha256(payload) FROM 1 FOR 16);
    digest := set_byte(digest, 6, (get_byte(digest, 6) & 15) | 128);
    digest := set_byte(digest, 8, (get_byte(digest, 8) & 63) | 128);
    RETURN encode(digest, 'hex')::uuid;
END;
$function$;
-- +goose StatementEnd

-- One row per historical source/workout, with the source identity resolved for
-- this range. Context evidence is independent of qualifying source visits.
-- +goose StatementBegin
CREATE FUNCTION app.coverage_display_source_visits(target_account_id uuid, selected_start date, selected_end date)
RETURNS TABLE (
    source_kind text, source_id uuid, workout_id uuid, local_start_date date, started_at timestamptz,
    broad_class text, name text, locality_name text, region_id text, region_name text,
    minimum_longitude double precision, minimum_latitude double precision,
    maximum_longitude double precision, maximum_latitude double precision,
    range_minimum_longitude double precision, range_minimum_latitude double precision,
    range_maximum_longitude double precision, range_maximum_latitude double precision
) LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, app SET jit = off AS $function$
    WITH eligible AS MATERIALIZED (
        SELECT workout.id, workout.local_start_date, workout.started_at
        FROM app.workouts workout
        JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
        JOIN app.workout_coverage_states coverage ON coverage.workout_id = workout.id AND coverage.account_id = workout.account_id
        WHERE workout.account_id = target_account_id AND workout.deletion_requested_at IS NULL
            AND workout.local_start_date IS NOT NULL AND route.route IS NOT NULL
            AND coverage.applied_route_input_revision IS NOT NULL
    ), path_visits AS MATERIALIZED (
        SELECT eligible.id workout_id, eligible.local_start_date, eligible.started_at, partial.entity_id,
            min(partial.region_id COLLATE "C") region_id, bool_or(partial.education_key_present) education_key_present,
            min(partial.education_name COLLATE "C") education_name, min(path.broad_class COLLATE "C") broad_class,
            min(path.name COLLATE "C") name, min(path.locality_name COLLATE "C") locality_name,
            min(partial.minimum_longitude) minimum_longitude, min(partial.minimum_latitude) minimum_latitude,
            max(partial.maximum_longitude) maximum_longitude, max(partial.maximum_latitude) maximum_latitude
        FROM eligible JOIN app.workout_path_display_partials partial
            ON partial.account_id = target_account_id AND partial.workout_id = eligible.id
        JOIN app.coverage_paths path ON path.account_id = partial.account_id AND path.logical_path_id = partial.logical_path_id
        WHERE path.name IS NOT NULL OR partial.qualifies_without_name
        GROUP BY eligible.id, eligible.local_start_date, eligible.started_at, partial.entity_id
    ), path_identity AS MATERIALIZED (
        SELECT visit.entity_id,
            CASE WHEN bool_or(visit.education_key_present) THEN 'other' ELSE min(visit.broad_class COLLATE "C") END broad_class,
            COALESCE(min(visit.education_name COLLATE "C"), min(visit.name COLLATE "C")) name,
            min(visit.locality_name COLLATE "C") locality_name,
            bool_or(visit.local_start_date BETWEEN selected_start AND selected_end) has_range,
            COALESCE(min(visit.region_id COLLATE "C") FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end),
                min(visit.region_id COLLATE "C")) region_id,
            min(visit.minimum_longitude) FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end) range_minimum_longitude,
            min(visit.minimum_latitude) FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end) range_minimum_latitude,
            max(visit.maximum_longitude) FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end) range_maximum_longitude,
            max(visit.maximum_latitude) FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end) range_maximum_latitude
        FROM path_visits visit GROUP BY visit.entity_id
    ), path_context AS MATERIALIZED (
        SELECT context.entity_id,
            min(context.locality_name COLLATE "C") FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_locality,
            min(context.locality_name COLLATE "C") historical_locality
        FROM eligible JOIN app.workout_path_display_context context
            ON context.account_id = target_account_id AND context.workout_id = eligible.id
        GROUP BY context.entity_id
    ), park_visits AS MATERIALIZED (
        SELECT eligible.id workout_id, eligible.local_start_date, eligible.started_at,
            attribution.park_id entity_id, bounds.region_id,
            bounds.minimum_longitude, bounds.minimum_latitude, bounds.maximum_longitude, bounds.maximum_latitude
        FROM eligible JOIN app.workout_park_attributions attribution
            ON attribution.account_id = target_account_id AND attribution.workout_id = eligible.id
        LEFT JOIN app.workout_park_display_bounds bounds ON bounds.account_id = attribution.account_id
            AND bounds.workout_id = attribution.workout_id AND bounds.park_id = attribution.park_id
    ), park_bounds AS MATERIALIZED (
        -- Independent evidence can supply current bounds/region even when this
        -- source has only historical attributions. Never synthesize a region.
        SELECT bounds.park_id,
            min(bounds.region_id COLLATE "C") FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_region_id,
            min(bounds.region_id COLLATE "C") historical_region_id,
            min(bounds.minimum_longitude) FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_minimum_longitude,
            min(bounds.minimum_latitude) FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_minimum_latitude,
            max(bounds.maximum_longitude) FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_maximum_longitude,
            max(bounds.maximum_latitude) FILTER (WHERE eligible.local_start_date BETWEEN selected_start AND selected_end) range_maximum_latitude
        FROM eligible JOIN app.workout_park_display_bounds bounds
            ON bounds.account_id = target_account_id AND bounds.workout_id = eligible.id
        GROUP BY bounds.park_id
    ), park_identity AS MATERIALIZED (
        SELECT visit.entity_id,
            COALESCE(min(visit.region_id COLLATE "C") FILTER (WHERE visit.local_start_date BETWEEN selected_start AND selected_end),
                bounds.range_region_id, min(visit.region_id COLLATE "C"), bounds.historical_region_id) region_id
        FROM park_visits visit LEFT JOIN park_bounds bounds ON bounds.park_id = visit.entity_id
        GROUP BY visit.entity_id, bounds.range_region_id, bounds.historical_region_id
    ), resolved_sources AS (
        SELECT 'path'::text source_kind, visit.entity_id source_id, visit.workout_id, visit.local_start_date, visit.started_at,
            identity.broad_class, identity.name,
            COALESCE(context.range_locality, CASE WHEN NOT identity.has_range THEN context.historical_locality END,
                identity.locality_name) locality_name, identity.region_id,
            visit.minimum_longitude, visit.minimum_latitude, visit.maximum_longitude, visit.maximum_latitude,
            identity.range_minimum_longitude, identity.range_minimum_latitude, identity.range_maximum_longitude, identity.range_maximum_latitude
        FROM path_visits visit JOIN path_identity identity USING (entity_id)
        LEFT JOIN path_context context USING (entity_id)
        UNION ALL
        SELECT 'park'::text, visit.entity_id, visit.workout_id, visit.local_start_date, visit.started_at,
            'park'::text, park.name, park.locality_name, identity.region_id,
            visit.minimum_longitude, visit.minimum_latitude, visit.maximum_longitude, visit.maximum_latitude,
            bounds.range_minimum_longitude, bounds.range_minimum_latitude, bounds.range_maximum_longitude, bounds.range_maximum_latitude
        FROM park_visits visit JOIN park_identity identity USING (entity_id)
        JOIN app.coverage_parks park ON park.account_id = target_account_id AND park.park_id = visit.entity_id
        LEFT JOIN park_bounds bounds ON bounds.park_id = visit.entity_id
    )
    SELECT source.source_kind, source.source_id, source.workout_id, source.local_start_date, source.started_at,
        source.broad_class, source.name,
        -- Match localityName ?? regionName ?? coverageRegionLabel(regionId),
        -- including a non-NULL empty locality. Region slugs are ASCII; Unicode
        -- case conversion is nevertheless pinned to the same builtin collation.
        COALESCE(source.locality_name, catalog.display_name,
            (SELECT string_agg(upper(left(part.word, 1) COLLATE pg_catalog.pg_c_utf8) ||
                    lower(substr(part.word, 2) COLLATE pg_catalog.pg_c_utf8), ' ' ORDER BY part.position)
                FROM regexp_split_to_table(regexp_replace(source.region_id, '^.*:', ''), '[-_]+')
                    WITH ORDINALITY part(word, position) WHERE part.word <> ''), source.region_id) locality_name,
        source.region_id, catalog.display_name region_name,
        source.minimum_longitude, source.minimum_latitude, source.maximum_longitude, source.maximum_latitude,
        source.range_minimum_longitude, source.range_minimum_latitude, source.range_maximum_longitude, source.range_maximum_latitude
    FROM resolved_sources source
    LEFT JOIN app.coverage_region_catalog catalog ON catalog.region_id = source.region_id
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.enqueue_coverage_display_invalidations(target_accounts uuid[]) RETURNS void
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    INSERT INTO app.coverage_display_invalidations(txid, account_id)
    SELECT DISTINCT txid_current(), account_id FROM unnest(target_accounts) account_id
    WHERE account_id IS NOT NULL ORDER BY account_id ON CONFLICT DO NOTHING
$function$;
-- +goose StatementEnd

-- Keep 028's four deferred generation triggers, but coalesce their public bump
-- with metadata/evidence invalidations rather than incrementing per row.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.advance_account_data_generation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE old_account uuid; new_account uuid;
BEGIN
    IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'updated_at') THEN
        RETURN NEW;
    END IF;
    IF TG_OP <> 'INSERT' THEN old_account := OLD.account_id; END IF;
    IF TG_OP <> 'DELETE' THEN new_account := NEW.account_id; END IF;
    PERFORM app.enqueue_coverage_display_invalidations(ARRAY[old_account, new_account]);
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- Only the first actual precomputation account-key insert invokes this bridge, not every
-- match. Leave the precomputation deferred flush, draining and subsequent expansion intact.
-- +goose StatementBegin
CREATE FUNCTION app.queue_coverage_display_source_invalidation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    PERFORM app.enqueue_coverage_display_invalidations(ARRAY[NEW.account_id]);
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER coverage_display_dirty_canonical_after_insert
    AFTER INSERT ON app.coverage_display_dirty FOR EACH ROW WHEN (NEW.scope = 'account')
    EXECUTE FUNCTION app.queue_coverage_display_source_invalidation();

-- +goose StatementBegin
CREATE FUNCTION app.queue_coverage_display_metadata_invalidation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE old_row jsonb; new_row jsonb; old_account uuid; new_account uuid; old_region text; new_region text;
BEGIN
    IF TG_OP <> 'INSERT' THEN old_row := to_jsonb(OLD); END IF;
    IF TG_OP <> 'DELETE' THEN new_row := to_jsonb(NEW); END IF;
    IF TG_OP = 'UPDATE' THEN
        IF TG_TABLE_NAME = 'coverage_paths' THEN
            IF (OLD.account_id, OLD.logical_path_id, OLD.name, OLD.locality_name, OLD.broad_class)
                IS NOT DISTINCT FROM (NEW.account_id, NEW.logical_path_id, NEW.name, NEW.locality_name, NEW.broad_class) THEN RETURN NULL; END IF;
        ELSIF TG_TABLE_NAME = 'coverage_parks' THEN
            IF (OLD.account_id, OLD.park_id, OLD.name, OLD.locality_name, OLD.park_kind)
                IS NOT DISTINCT FROM (NEW.account_id, NEW.park_id, NEW.name, NEW.locality_name, NEW.park_kind) THEN RETURN NULL; END IF;
        ELSIF TG_TABLE_NAME = 'workout_park_attributions' THEN
            IF (OLD.account_id, OLD.workout_id, OLD.park_id) IS NOT DISTINCT FROM
                (NEW.account_id, NEW.workout_id, NEW.park_id) THEN RETURN NULL; END IF;
        ELSIF TG_TABLE_NAME = 'workout_coverage_states' THEN
            IF (OLD.account_id, OLD.workout_id, OLD.applied_route_input_revision IS NOT NULL) IS NOT DISTINCT FROM
                (NEW.account_id, NEW.workout_id, NEW.applied_route_input_revision IS NOT NULL) THEN RETURN NULL; END IF;
        ELSIF TG_TABLE_NAME = 'coverage_region_catalog' THEN
            IF (OLD.region_id, OLD.display_name) IS NOT DISTINCT FROM (NEW.region_id, NEW.display_name) THEN RETURN NULL; END IF;
        ELSIF (old_row - 'updated_at') IS NOT DISTINCT FROM (new_row - 'updated_at') THEN
            RETURN NULL;
        END IF;
    END IF;
    IF TG_TABLE_NAME = 'coverage_region_catalog' THEN
        old_region := old_row->>'region_id'; new_region := new_row->>'region_id';
        -- Include context-only evidence and accounts with a previously empty scope.
        INSERT INTO app.coverage_display_invalidations(txid, account_id)
        SELECT DISTINCT txid_current(), evidence.account_id FROM (
            SELECT account_id FROM app.workout_path_display_partials WHERE region_id IN (old_region, new_region)
            UNION SELECT account_id FROM app.workout_park_display_bounds WHERE region_id IN (old_region, new_region)
            UNION SELECT account_id FROM app.coverage_display_members WHERE region_id IN (old_region, new_region)
        ) evidence ORDER BY evidence.account_id ON CONFLICT DO NOTHING;
    ELSE
        old_account := (old_row->>'account_id')::uuid; new_account := (new_row->>'account_id')::uuid;
        PERFORM app.enqueue_coverage_display_invalidations(ARRAY[old_account, new_account]);
    END IF;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER coverage_paths_canonical_after_write AFTER INSERT OR UPDATE OR DELETE ON app.coverage_paths
    FOR EACH ROW EXECUTE FUNCTION app.queue_coverage_display_metadata_invalidation();
CREATE TRIGGER coverage_parks_canonical_after_write AFTER INSERT OR UPDATE OR DELETE ON app.coverage_parks
    FOR EACH ROW EXECUTE FUNCTION app.queue_coverage_display_metadata_invalidation();
CREATE TRIGGER workout_park_attributions_canonical_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workout_park_attributions
    FOR EACH ROW EXECUTE FUNCTION app.queue_coverage_display_metadata_invalidation();
CREATE TRIGGER workout_coverage_states_canonical_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workout_coverage_states
    FOR EACH ROW EXECUTE FUNCTION app.queue_coverage_display_metadata_invalidation();
CREATE TRIGGER coverage_region_catalog_canonical_after_write AFTER INSERT OR UPDATE OR DELETE ON app.coverage_region_catalog
    FOR EACH ROW EXECUTE FUNCTION app.queue_coverage_display_metadata_invalidation();

-- +goose StatementBegin
CREATE FUNCTION app.flush_coverage_display_invalidations() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE target uuid;
BEGIN
    FOR target IN SELECT account_id FROM app.coverage_display_invalidations
        WHERE txid = txid_current() ORDER BY account_id LOOP
        -- All flush paths take generation before revision, including metadata-
        -- only writes; otherwise an evidence flush could invert these locks.
        UPDATE app.account_data_generations SET generation = generation WHERE account_id = target;
        -- This retains the precomputation barrier and fresh post-barrier segment expansion.
        PERFORM app.flush_coverage_display_precomputations(target);
        UPDATE app.coverage_display_revisions SET revision = revision + 1,
            last_invalidated_transaction_id = txid_current()
        WHERE account_id = target AND last_invalidated_transaction_id IS DISTINCT FROM txid_current();
        IF FOUND THEN
            UPDATE app.account_data_generations SET generation =
                CASE WHEN generation = 9223372036854775807 THEN 1 ELSE generation + 1 END,
                updated_at = transaction_timestamp() WHERE account_id = target;
        END IF;
        DELETE FROM app.coverage_display_invalidations WHERE txid = txid_current() AND account_id = target;
    END LOOP;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER coverage_display_invalidations_flush
    AFTER INSERT ON app.coverage_display_invalidations DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION app.flush_coverage_display_invalidations();

-- +goose StatementBegin
CREATE FUNCTION app.seed_coverage_display_revision() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    INSERT INTO app.coverage_display_revisions(account_id) VALUES (NEW.id);
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER accounts_coverage_display_revision_after_insert AFTER INSERT ON app.accounts
    FOR EACH ROW EXECUTE FUNCTION app.seed_coverage_display_revision();
INSERT INTO app.coverage_display_revisions(account_id) SELECT id FROM app.accounts;
UPDATE app.account_data_generations SET generation =
    CASE WHEN generation = 9223372036854775807 THEN 1 ELSE generation + 1 END,
    updated_at = transaction_timestamp();

-- The only mutable selection field is the new private context binding. In
-- particular FK SET NULL must not null account_id or weaken scope immutability.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.validate_map_selection() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF (to_jsonb(OLD) - 'coverage_display_context_id') IS DISTINCT FROM
            (to_jsonb(NEW) - 'coverage_display_context_id') THEN
            RAISE EXCEPTION 'map selections are immutable' USING ERRCODE = '23514';
        END IF;
        IF NEW.coverage_display_context_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM app.coverage_display_contexts context
            JOIN app.coverage_display_revisions revision ON revision.account_id = context.account_id
            WHERE context.account_id = NEW.account_id AND context.id = NEW.coverage_display_context_id
                AND context.generation = NEW.generation AND context.revision = revision.revision AND context.ready
                AND context.start_date = NEW.start_date AND context.end_date = NEW.end_date
                AND context.policy_version = 'coverage-display-identity-v1' AND context.label_locale = 'en'
        ) THEN RAISE EXCEPTION 'invalid coverage display context' USING ERRCODE = '23514'; END IF;
        RETURN NEW;
    END IF;
    IF NEW.coverage_display_context_id IS NOT NULL THEN
        RAISE EXCEPTION 'prepare coverage display context after selection capture' USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.sessions session_row
        JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id = principal.id
        JOIN app.accounts account ON account.id = account_owner.account_id
        JOIN app.account_data_generations data_generation ON data_generation.account_id = account.id
        WHERE session_row.id = NEW.session_id AND account_owner.account_id = NEW.account_id
            AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
            AND principal.disabled_at IS NULL AND account.state = 'active'
            AND NEW.expires_at <= session_row.expires_at AND NEW.expires_at > transaction_timestamp()
            AND data_generation.generation = NEW.generation
    ) THEN RAISE EXCEPTION 'invalid map selection scope' USING ERRCODE = '42501'; END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- Context binding/GC changes no focus input. Revalidating old focus on a stale
-- selection could otherwise make FK SET NULL fail after a workout/date change.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.validate_map_selection_focus() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'coverage_display_context_id') IS NOT DISTINCT FROM
        (to_jsonb(NEW) - 'coverage_display_context_id') THEN RETURN NEW; END IF;
    IF NEW.focused_workout_id IS NULL THEN RETURN NEW; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selection_workouts selected_workout
        JOIN app.workouts workout ON workout.id = selected_workout.workout_id AND workout.account_id = selected_workout.account_id
        JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
        WHERE selected_workout.account_id = NEW.account_id AND selected_workout.selection_id = NEW.id
            AND selected_workout.workout_id = NEW.focused_workout_id
            AND workout.local_start_date BETWEEN NEW.start_date AND NEW.end_date
            AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
    ) THEN RAISE EXCEPTION 'invalid focused map selection workout' USING ERRCODE = '23514'; END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- Fresh capability validation is shared by prepare and every canonical reader.
-- +goose StatementBegin
CREATE FUNCTION app.coverage_display_selection_scope(target_account_id uuid, target_session_id uuid,
    target_selection_id uuid, target_generation bigint)
RETURNS TABLE (start_date date, end_date date, revision bigint, bound_context_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT selection.start_date, selection.end_date, revision.revision, selection.coverage_display_context_id
    FROM app.map_selections selection
    JOIN app.sessions session_row ON session_row.id = selection.session_id
    JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
    JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
    JOIN app.accounts account ON account.id = account_owner.account_id
    JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
    JOIN app.coverage_display_revisions revision ON revision.account_id = selection.account_id
    WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
        AND selection.session_id = target_session_id AND selection.generation = target_generation
        AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
        AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
        AND principal.disabled_at IS NULL AND account.state = 'active'
        AND target_account_id = app.current_account_id() AND target_session_id = app.current_session_id()
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.coverage_display_authorized_context(target_account_id uuid, target_session_id uuid,
    target_selection_id uuid, target_generation bigint) RETURNS uuid
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE scope record; result uuid;
BEGIN
    SELECT * INTO scope FROM app.coverage_display_selection_scope(target_account_id, target_session_id,
        target_selection_id, target_generation);
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT context.id INTO result FROM app.coverage_display_contexts context
    WHERE context.account_id = target_account_id AND context.id = scope.bound_context_id
        AND context.generation = target_generation AND context.revision = scope.revision
        AND context.start_date = scope.start_date AND context.end_date = scope.end_date AND context.ready
        AND context.policy_version = 'coverage-display-identity-v1' AND context.label_locale = 'en';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'coverage display context is not ready; prepare selection again' USING ERRCODE = '55000';
    END IF;
    RETURN result;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.prepare_coverage_display_context(target_account_id uuid, target_session_id uuid,
    target_selection_id uuid, target_generation bigint)
RETURNS TABLE (context_id uuid, display_revision bigint, policy_version text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app SET jit = off AS $function$
DECLARE scope record; prepared_id uuid; inserted_id uuid;
BEGIN
    SELECT * INTO scope FROM app.coverage_display_selection_scope(target_account_id, target_session_id,
        target_selection_id, target_generation);
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid map selection scope' USING ERRCODE = '42501'; END IF;
    -- No account/generation lock: this uses the caller's RR selection snapshot.
    INSERT INTO app.coverage_display_contexts(account_id, generation, revision, start_date, end_date)
    VALUES (target_account_id, target_generation, scope.revision, scope.start_date, scope.end_date)
    ON CONFLICT ON CONSTRAINT coverage_display_contexts_scope_key DO NOTHING
    RETURNING id INTO inserted_id;
    IF inserted_id IS NULL THEN
        SELECT context.id INTO prepared_id FROM app.coverage_display_contexts context
        WHERE context.account_id = target_account_id AND context.generation = target_generation
            AND context.revision = scope.revision AND context.start_date = scope.start_date AND context.end_date = scope.end_date
            AND context.policy_version = 'coverage-display-identity-v1' AND context.label_locale = 'en' AND context.ready;
        IF NOT FOUND THEN RAISE EXCEPTION 'coverage display context is not ready' USING ERRCODE = '55000'; END IF;
    ELSE
        prepared_id := inserted_id;
        WITH source_visits AS MATERIALIZED (
            SELECT * FROM app.coverage_display_source_visits(target_account_id, scope.start_date, scope.end_date)
        ), identities AS (
            SELECT DISTINCT source_kind, source_id, broad_class, name, locality_name, region_id, region_name,
                range_minimum_longitude, range_minimum_latitude, range_maximum_longitude, range_maximum_latitude
            FROM source_visits
        ), normalized AS (
            SELECT identity.*, COALESCE(region_id, '') key_region,
                app.coverage_display_normalize(COALESCE(name, '')) normalized_name,
                app.coverage_display_normalize(COALESCE(locality_name, '')) normalized_locality
            FROM identities identity
        ), keyed AS (
            SELECT normalized.*, CASE WHEN normalized_name = '' THEN source_kind || '/' || source_id::text ELSE '' END key_discriminator
            FROM normalized
        ), member_rows AS (
            INSERT INTO app.coverage_display_members(account_id, context_id, source_kind, source_id, canonical_id,
                broad_class, name, locality_name, region_id, region_name,
                key_region, normalized_name, normalized_locality, key_discriminator,
                range_minimum_longitude, range_minimum_latitude, range_maximum_longitude, range_maximum_latitude)
            SELECT target_account_id, prepared_id, source_kind, source_id,
                app.coverage_display_identity(key_region, normalized_name, normalized_locality, key_discriminator),
                broad_class, name, locality_name, region_id, region_name,
                key_region, normalized_name, normalized_locality, key_discriminator,
                range_minimum_longitude, range_minimum_latitude, range_maximum_longitude, range_maximum_latitude FROM keyed
            RETURNING source_kind, source_id, canonical_id
        )
        INSERT INTO app.coverage_display_visits(account_id, context_id, canonical_id, workout_id, local_start_date,
            started_at, minimum_longitude, minimum_latitude, maximum_longitude, maximum_latitude)
        SELECT target_account_id, prepared_id, member.canonical_id, visit.workout_id, visit.local_start_date, visit.started_at,
            min(visit.minimum_longitude), min(visit.minimum_latitude), max(visit.maximum_longitude), max(visit.maximum_latitude)
        FROM source_visits visit JOIN member_rows member USING (source_kind, source_id)
        GROUP BY member.canonical_id, visit.workout_id, visit.local_start_date, visit.started_at;

        IF EXISTS (
            SELECT 1 FROM app.coverage_display_members member
            JOIN app.coverage_display_members other ON other.account_id = member.account_id AND other.canonical_id = member.canonical_id
            WHERE member.account_id = target_account_id AND member.context_id = prepared_id
                AND (member.key_region, member.normalized_name, member.normalized_locality, member.key_discriminator)
                    IS DISTINCT FROM (other.key_region, other.normalized_name, other.normalized_locality, other.key_discriminator)
        ) THEN RAISE EXCEPTION 'canonical coverage display UUID collision' USING ERRCODE = '23505'; END IF;

        -- Summarize visits once, then probe only each canonical group's members.
        WITH visits AS MATERIALIZED (
            SELECT visit.*, visit.local_start_date BETWEEN scope.start_date AND scope.end_date in_range
            FROM app.coverage_display_visits visit WHERE visit.account_id = target_account_id AND visit.context_id = prepared_id
        ), summaries AS MATERIALIZED (
            SELECT visit.canonical_id, count(*) FILTER (WHERE in_range) range_workout_count,
                min(local_start_date) FILTER (WHERE in_range) range_first_date,
                (array_agg(workout_id ORDER BY local_start_date, started_at, workout_id) FILTER (WHERE in_range))[1] range_first_workout_id,
                max(local_start_date) FILTER (WHERE in_range) range_latest_date,
                (array_agg(workout_id ORDER BY local_start_date DESC, started_at, workout_id) FILTER (WHERE in_range))[1] range_latest_workout_id,
                count(*) all_time_workout_count, min(local_start_date) all_time_first_date,
                (array_agg(workout_id ORDER BY local_start_date, started_at, workout_id))[1] all_time_first_workout_id,
                max(local_start_date) all_time_latest_date,
                (array_agg(workout_id ORDER BY local_start_date DESC, started_at, workout_id))[1] all_time_latest_workout_id
            FROM visits visit GROUP BY visit.canonical_id
        )
        INSERT INTO app.coverage_display_entities(account_id, context_id, canonical_id,
            entity_kind, broad_class, name, locality_name, region_id, region_name,
            range_workout_count, range_first_date, range_first_workout_id, range_latest_date, range_latest_workout_id,
            all_time_workout_count, all_time_first_date, all_time_first_workout_id, all_time_latest_date, all_time_latest_workout_id,
            minimum_longitude, minimum_latitude, maximum_longitude, maximum_latitude)
        SELECT target_account_id, prepared_id, representative.canonical_id,
            representative.source_kind, representative.broad_class, representative.name,
            representative.locality_name, representative.region_id, representative.region_name,
            summary.range_workout_count, summary.range_first_date, summary.range_first_workout_id,
            summary.range_latest_date, summary.range_latest_workout_id, summary.all_time_workout_count,
            summary.all_time_first_date, summary.all_time_first_workout_id, summary.all_time_latest_date, summary.all_time_latest_workout_id,
            representative.canonical_minimum_longitude, representative.canonical_minimum_latitude,
            representative.canonical_maximum_longitude, representative.canonical_maximum_latitude
        FROM summaries summary
        JOIN LATERAL (
            -- The window covers every scoped member before representative LIMIT,
            -- including historical-only parks with independent range geometry.
            SELECT member.*,
                min(member.range_minimum_longitude) OVER () canonical_minimum_longitude,
                min(member.range_minimum_latitude) OVER () canonical_minimum_latitude,
                max(member.range_maximum_longitude) OVER () canonical_maximum_longitude,
                max(member.range_maximum_latitude) OVER () canonical_maximum_latitude
            FROM app.coverage_display_members member
            WHERE member.account_id = target_account_id AND member.context_id = prepared_id
                AND member.canonical_id = summary.canonical_id
            ORDER BY (member.source_kind = 'park') DESC,
                member.name COLLATE "C", member.locality_name COLLATE "C", member.broad_class COLLATE "C", member.source_id
            LIMIT 1
        ) representative ON true
        -- Keep all memberships/visits, but never publish an unscannable visible
        -- entity. A valid bound from any member keeps a combined group visible.
        WHERE summary.range_workout_count = 0 OR (representative.region_id IS NOT NULL
            AND representative.canonical_minimum_longitude IS NOT NULL AND representative.canonical_minimum_latitude IS NOT NULL
            AND representative.canonical_maximum_longitude IS NOT NULL AND representative.canonical_maximum_latitude IS NOT NULL);
        UPDATE app.coverage_display_contexts SET ready = true WHERE account_id = target_account_id AND id = prepared_id;
    END IF;
    UPDATE app.map_selections SET coverage_display_context_id = prepared_id
        WHERE account_id = target_account_id AND id = target_selection_id;

    IF inserted_id IS NOT NULL THEN
        -- Only a new build runs GC. Warm reuse writes its own selection, never
        -- a shared ready context, avoiding unrelated RR write/write failures.
        -- Keep eight reusable scopes plus all actively referenced valid contexts.
        WITH unpinned AS MATERIALIZED (
            SELECT context.id, context.generation, context.revision, context.last_used_at,
                row_number() OVER (ORDER BY context.last_used_at DESC, context.id) reusable_rank
            FROM app.coverage_display_contexts context WHERE context.account_id = target_account_id
                AND NOT EXISTS (SELECT 1 FROM app.map_selections selection
                    JOIN app.sessions session_row ON session_row.id = selection.session_id
                    JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
                    JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
                    WHERE selection.account_id = context.account_id AND selection.coverage_display_context_id = context.id
                        AND selection.expires_at > transaction_timestamp() AND selection.generation = target_generation
                        AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
                        AND principal.disabled_at IS NULL
                        AND context.generation = target_generation AND context.revision = scope.revision AND context.ready)
        )
        DELETE FROM app.coverage_display_contexts context USING unpinned
        WHERE context.account_id = target_account_id AND context.id = unpinned.id
            AND (unpinned.generation <> target_generation OR unpinned.revision <> scope.revision
                OR unpinned.last_used_at < transaction_timestamp() - interval '1 day' OR unpinned.reusable_rank > 8);
    END IF;

    RETURN QUERY SELECT prepared_id, scope.revision, 'coverage-display-identity-v1'::text;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.map_selection_coverage_entities (
    target_account_id uuid, target_session_id uuid, target_selection_id uuid, target_generation bigint,
    target_search text DEFAULT NULL
) RETURNS TABLE (
    entity_id uuid, entity_kind text, broad_class text, name text, locality_name text, region_id text, region_name text,
    range_workout_count bigint, range_first_date date, range_first_workout_id uuid, range_latest_date date, range_latest_workout_id uuid,
    all_time_workout_count bigint, all_time_first_date date, all_time_first_workout_id uuid, all_time_latest_date date, all_time_latest_workout_id uuid,
    minimum_longitude double precision, minimum_latitude double precision, maximum_longitude double precision, maximum_latitude double precision
) LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app SET jit = off AS $function$
DECLARE authorized_id uuid;
BEGIN
    authorized_id := app.coverage_display_authorized_context(target_account_id, target_session_id, target_selection_id, target_generation);
    IF authorized_id IS NULL THEN RETURN; END IF;
    RETURN QUERY SELECT entity.entity_id, entity.entity_kind, entity.broad_class, entity.name, entity.locality_name,
        entity.region_id, entity.region_name, entity.range_workout_count, entity.range_first_date, entity.range_first_workout_id,
        entity.range_latest_date, entity.range_latest_workout_id, entity.all_time_workout_count,
        entity.all_time_first_date, entity.all_time_first_workout_id, entity.all_time_latest_date, entity.all_time_latest_workout_id,
        entity.minimum_longitude, entity.minimum_latitude, entity.maximum_longitude, entity.maximum_latitude
    FROM app.coverage_display_entities entity WHERE entity.account_id = target_account_id
        AND entity.context_id = authorized_id AND entity.range_workout_count > 0
        AND (NULLIF(target_search, '') IS NULL OR strpos(app.coverage_display_normalize(concat_ws(' ',
            entity.entity_id::text, entity.entity_kind, entity.broad_class, entity.name, entity.locality_name,
            entity.region_name, entity.region_id)), app.coverage_display_normalize(target_search)) > 0);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.map_selection_coverage_entity_stats (
    target_account_id uuid, target_session_id uuid, target_selection_id uuid, target_generation bigint,
    target_entity_kind text, target_entity_id uuid
) RETURNS TABLE (
    entity_id uuid, entity_kind text, broad_class text, name text, locality_name text, region_id text, region_name text,
    range_workout_count bigint, range_first_date date, range_first_workout_id uuid, range_latest_date date, range_latest_workout_id uuid,
    all_time_workout_count bigint, all_time_first_date date, all_time_first_workout_id uuid, all_time_latest_date date, all_time_latest_workout_id uuid,
    minimum_longitude double precision, minimum_latitude double precision, maximum_longitude double precision, maximum_latitude double precision
) LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app SET jit = off AS $function$
DECLARE authorized_id uuid;
BEGIN
    authorized_id := app.coverage_display_authorized_context(target_account_id, target_session_id, target_selection_id, target_generation);
    IF authorized_id IS NULL THEN RETURN; END IF;
    RETURN QUERY SELECT entity.entity_id, entity.entity_kind, entity.broad_class, entity.name, entity.locality_name,
        entity.region_id, entity.region_name, entity.range_workout_count, entity.range_first_date, entity.range_first_workout_id,
        entity.range_latest_date, entity.range_latest_workout_id, entity.all_time_workout_count,
        entity.all_time_first_date, entity.all_time_first_workout_id, entity.all_time_latest_date, entity.all_time_latest_workout_id,
        entity.minimum_longitude, entity.minimum_latitude, entity.maximum_longitude, entity.maximum_latitude
    FROM app.coverage_display_entities entity WHERE entity.account_id = target_account_id AND entity.context_id = authorized_id
        AND entity.canonical_id = target_entity_id AND entity.entity_kind = target_entity_kind AND entity.range_workout_count > 0;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.map_selection_coverage_entity_members(target_account_id uuid, target_session_id uuid,
    target_selection_id uuid, target_generation bigint, target_entity_kind text, target_entity_id uuid)
RETURNS TABLE (source_kind text, source_id uuid)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE authorized_id uuid;
BEGIN
    authorized_id := app.coverage_display_authorized_context(target_account_id, target_session_id, target_selection_id, target_generation);
    IF authorized_id IS NULL THEN RETURN; END IF;
    RETURN QUERY SELECT member.source_kind, member.source_id
    FROM app.coverage_display_entities entity JOIN app.coverage_display_members member
        ON member.account_id = entity.account_id AND member.context_id = entity.context_id AND member.canonical_id = entity.canonical_id
    WHERE entity.account_id = target_account_id AND entity.context_id = authorized_id AND entity.canonical_id = target_entity_id
        AND entity.entity_kind = target_entity_kind AND entity.range_workout_count > 0
    ORDER BY member.source_kind COLLATE "C", member.source_id;
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.coverage_display_revisions OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_display_contexts OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_display_members OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_display_visits OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_display_entities OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_display_invalidations OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_display_normalize(text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_display_identity(text, text, text, text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_display_source_visits(uuid, date, date) OWNER TO workouts_security_owner;
ALTER FUNCTION app.enqueue_coverage_display_invalidations(uuid[]) OWNER TO workouts_security_owner;
ALTER FUNCTION app.advance_account_data_generation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.mark_coverage_display_dirty() OWNER TO workouts_security_owner;
ALTER FUNCTION app.queue_coverage_display_source_invalidation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.queue_coverage_display_metadata_invalidation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.flush_coverage_display_invalidations() OWNER TO workouts_security_owner;
ALTER FUNCTION app.seed_coverage_display_revision() OWNER TO workouts_security_owner;
ALTER FUNCTION app.validate_map_selection() OWNER TO workouts_security_owner;
ALTER FUNCTION app.validate_map_selection_focus() OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_display_selection_scope(uuid, uuid, uuid, bigint) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_display_authorized_context(uuid, uuid, uuid, bigint) OWNER TO workouts_security_owner;
ALTER FUNCTION app.prepare_coverage_display_context(uuid, uuid, uuid, bigint) OWNER TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_entities(uuid, uuid, uuid, bigint, text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_entity_members(uuid, uuid, uuid, bigint, text, uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT UPDATE(coverage_display_context_id) ON app.map_selections TO workouts_security_owner;
REVOKE ALL ON app.coverage_display_revisions, app.coverage_display_contexts, app.coverage_display_members,
    app.coverage_display_visits, app.coverage_display_entities, app.coverage_display_invalidations
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
REVOKE ALL ON FUNCTION app.coverage_display_normalize(text), app.coverage_display_identity(text, text, text, text),
    app.coverage_display_source_visits(uuid, date, date), app.enqueue_coverage_display_invalidations(uuid[]),
    app.advance_account_data_generation(), app.mark_coverage_display_dirty(), app.queue_coverage_display_source_invalidation(),
    app.queue_coverage_display_metadata_invalidation(), app.flush_coverage_display_invalidations(),
    app.seed_coverage_display_revision(), app.validate_map_selection(), app.validate_map_selection_focus(),
    app.coverage_display_selection_scope(uuid, uuid, uuid, bigint), app.coverage_display_authorized_context(uuid, uuid, uuid, bigint),
    app.prepare_coverage_display_context(uuid, uuid, uuid, bigint), app.map_selection_coverage_entities(uuid, uuid, uuid, bigint, text),
    app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid),
    app.map_selection_coverage_entity_members(uuid, uuid, uuid, bigint, text, uuid)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.prepare_coverage_display_context(uuid, uuid, uuid, bigint),
    app.map_selection_coverage_entities(uuid, uuid, uuid, bigint, text),
    app.map_selection_coverage_entity_stats(uuid, uuid, uuid, bigint, text, uuid),
    app.map_selection_coverage_entity_members(uuid, uuid, uuid, bigint, text, uuid) TO workouts_api;

-- Canonical geometry and tile readers are installed here.

-- CREATE OR REPLACE preserves the shipped API-only detail/focus and tiles-only
-- MVT ACLs. No generic capability helper is exposed to the network tile role.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.map_selection_coverage_entity_detail(
    target_account_id uuid,
    target_session_id uuid,
    target_selection_id uuid,
    target_generation bigint,
    target_entity_kind text,
    target_entity_id uuid
)
RETURNS TABLE (
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
    maximum_latitude double precision,
    geometry jsonb,
    fit_minimum_longitude double precision,
    fit_minimum_latitude double precision,
    fit_maximum_longitude double precision,
    fit_maximum_latitude double precision
)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off
AS $function$
    WITH metadata AS MATERIALIZED (
        SELECT * FROM app.map_selection_coverage_entity_stats(target_account_id, target_session_id,
            target_selection_id, target_generation, target_entity_kind, target_entity_id)
    )
    SELECT metadata.*, detail.*
    FROM metadata CROSS JOIN LATERAL (
        WITH members AS MATERIALIZED (
            SELECT member.source_kind, member.source_id
            FROM app.map_selections selection
            JOIN app.coverage_display_members member ON member.account_id = selection.account_id
                AND member.context_id = selection.coverage_display_context_id
            WHERE selection.account_id = target_account_id AND selection.id = target_selection_id
                AND member.canonical_id = metadata.entity_id
        ), eligible AS MATERIALIZED (
            SELECT workout.id FROM app.map_selections selection
            JOIN app.workouts workout ON workout.account_id = selection.account_id
                AND workout.local_start_date BETWEEN selection.start_date AND selection.end_date
            JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
            JOIN app.workout_coverage_states coverage ON coverage.workout_id = workout.id
                AND coverage.account_id = workout.account_id
            WHERE selection.account_id = target_account_id AND selection.id = target_selection_id
                AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
                AND coverage.applied_route_input_revision IS NOT NULL
        ), pieces AS (
            SELECT match.geom FROM eligible
            JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = eligible.id
            JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
                AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
            JOIN app.coverage_paths path ON path.account_id = match.account_id AND path.logical_path_id = match.logical_path_id
            JOIN members member ON member.source_kind = 'path'
                AND member.source_id = COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id)
            WHERE NOT (segment.highway = 'service' AND COALESCE(segment.tags->>'service', '') IN ('driveway', 'parking_aisle'))
                AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
                AND (path.name IS NOT NULL OR segment.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                    OR NOT (segment.tags ? 'workouts:park_id'))
            UNION ALL
            SELECT match.geom FROM eligible
            JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = eligible.id
            JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
                AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
            JOIN members member ON member.source_kind = 'park'
                AND segment.tags->>'workouts:park_id' = member.source_id::text
        ), aggregate AS (
            SELECT ST_UnaryUnion(ST_Collect(geom)) geom FROM pieces
        ), projected AS (
            SELECT geom, ST_Transform(geom, 3857) web_mercator FROM aggregate WHERE geom IS NOT NULL
        ), fitted AS (
            SELECT geom, ST_Transform(ST_SetSRID(ST_Envelope(ST_Expand(ST_Extent(web_mercator)::box2d,
                greatest((500 - ST_XMax(ST_Extent(web_mercator)::box2d) + ST_XMin(ST_Extent(web_mercator)::box2d)) / 2, 0),
                greatest((500 - ST_YMax(ST_Extent(web_mercator)::box2d) + ST_YMin(ST_Extent(web_mercator)::box2d)) / 2, 0))), 3857), 4326) fit
            FROM projected GROUP BY geom
        )
        SELECT ST_AsGeoJSON(fitted.geom)::jsonb AS geometry,
            ST_XMin(fitted.fit) AS fit_minimum_longitude, ST_YMin(fitted.fit) AS fit_minimum_latitude,
            ST_XMax(fitted.fit) AS fit_maximum_longitude, ST_YMax(fitted.fit) AS fit_maximum_latitude
        FROM fitted
        WHERE metadata.entity_kind = target_entity_kind AND target_entity_kind IN ('path', 'park')
        OFFSET 0
    ) detail
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.coverage_mvt(z integer, x integer, y integer, query_params json DEFAULT '{}'::json)
RETURNS bytea
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off
AS $function$
DECLARE
    target_account_id uuid;
    target_session_id uuid;
    target_selection_id uuid;
    target_generation bigint;
    display_context_id uuid;
    bounds geometry;
    tile bytea;
BEGIN
    IF z IS NULL OR x IS NULL OR y IS NULL OR z NOT BETWEEN 0 AND 22 OR x < 0 OR y < 0
        OR x >= (1::bigint << z) OR y >= (1::bigint << z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE = '22023';
    END IF;
    IF json_typeof(query_params) IS DISTINCT FROM 'object' OR
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

    -- Martin supplies the four-part capability, not an app.account_id setting.
    -- Keep this full session/account/selection check inside the tiles-only entry
    -- point; the private API helper deliberately still requires account GUCs.
    SELECT context.id INTO display_context_id
    FROM app.map_selections selection
    JOIN app.sessions session_row ON session_row.id = selection.session_id
    JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
    JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
    JOIN app.accounts account ON account.id = account_owner.account_id
    JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
    JOIN app.coverage_display_contexts context ON context.account_id = selection.account_id
        AND context.id = selection.coverage_display_context_id
    JOIN app.coverage_display_revisions revision ON revision.account_id = selection.account_id
    WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
        AND selection.session_id = target_session_id AND selection.generation = target_generation
        AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
        AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
        AND principal.disabled_at IS NULL AND account.state = 'active'
        AND context.generation = target_generation AND context.revision = revision.revision
        AND context.start_date = selection.start_date AND context.end_date = selection.end_date
        AND context.ready AND context.policy_version = 'coverage-display-identity-v1' AND context.label_locale = 'en';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE = '42501';
    END IF;

    bounds := ST_TileEnvelope(z, x, y);
    WITH selected AS MATERIALIZED (
        SELECT selection.start_date, selection.end_date, selection.focused_workout_id
        FROM app.map_selections selection WHERE selection.account_id = target_account_id AND selection.id = target_selection_id
    ), eligible AS MATERIALIZED (
        SELECT workout.id FROM app.workouts workout
        JOIN app.workout_routes route ON route.account_id = workout.account_id AND route.workout_id = workout.id
        JOIN app.workout_coverage_states coverage ON coverage.account_id = workout.account_id AND coverage.workout_id = workout.id
        CROSS JOIN selected WHERE workout.account_id = target_account_id AND workout.deletion_requested_at IS NULL
            AND route.route IS NOT NULL AND coverage.applied_route_input_revision IS NOT NULL
            AND workout.local_start_date BETWEEN selected.start_date AND selected.end_date
    ), checked AS MATERIALIZED (
        SELECT match.*, segment.highway, segment.tags FROM app.map_selection_workouts selected_workout
        JOIN eligible ON eligible.id = selected_workout.workout_id
        JOIN app.workout_segment_matches match ON match.account_id = selected_workout.account_id
            AND match.workout_id = selected_workout.workout_id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id
            AND match.geom && ST_Transform(bounds, 4326)
    ), pieces AS MATERIALIZED (
        SELECT member.canonical_id, member.source_kind, member.source_id, checked.workout_id, checked.geom
        FROM checked
        JOIN app.coverage_paths path ON path.account_id = checked.account_id AND path.logical_path_id = checked.logical_path_id
        JOIN app.coverage_display_members member ON member.account_id = target_account_id
            AND member.context_id = display_context_id AND member.source_kind = 'path'
            AND member.source_id = COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id)
        WHERE NOT (checked.highway = 'service' AND COALESCE(checked.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR checked.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (checked.tags ? 'workouts:park_id'))
        UNION ALL
        SELECT member.canonical_id, member.source_kind, member.source_id, checked.workout_id, checked.geom
        FROM checked
        JOIN app.coverage_display_members member ON member.account_id = target_account_id
            AND member.context_id = display_context_id AND member.source_kind = 'park'
            AND member.source_id = (checked.tags->>'workouts:park_id')::uuid
        WHERE checked.tags ? 'workouts:park_id'
    ), shapes AS MATERIALIZED (
        SELECT canonical_id, source_kind, source_id, false focused, ST_UnaryUnion(ST_Collect(geom)) geom
        FROM pieces GROUP BY canonical_id, source_kind, source_id
        UNION ALL
        SELECT pieces.canonical_id, pieces.source_kind, pieces.source_id, true, ST_UnaryUnion(ST_Collect(pieces.geom))
        FROM pieces CROSS JOIN selected WHERE pieces.workout_id = selected.focused_workout_id
        GROUP BY pieces.canonical_id, pieces.source_kind, pieces.source_id
    ), features AS MATERIALIZED (
        SELECT shapes.source_kind, shapes.focused,
            upper(replace(entity.canonical_id::text, '-', '')) "entityId", entity.entity_kind "entityKind",
            upper(replace(shapes.source_id::text, '-', '')) "sourceEntityId", shapes.source_kind "sourceEntityKind",
            entity.broad_class "broadClass", entity.name, entity.locality_name "localityName", entity.region_id "regionId",
            entity.range_workout_count "rangeWorkoutCount", app.coverage_count_bucket(entity.range_workout_count) "countBucket",
            ST_AsMVTGeom(ST_Transform(shapes.geom, 3857), bounds::box2d, 4096, 64, true) geometry
        FROM shapes JOIN app.coverage_display_entities entity ON entity.account_id = target_account_id
            AND entity.context_id = display_context_id AND entity.canonical_id = shapes.canonical_id
        WHERE entity.range_workout_count > 0 AND ST_Intersects(ST_Transform(shapes.geom, 3857), bounds)
    )
    SELECT COALESCE((SELECT ST_AsMVT(rows, 'coverage', 4096, 'geometry') FROM (
        SELECT "entityId", "entityKind", "sourceEntityId", "sourceEntityKind", "broadClass", name, "localityName", "regionId",
            "rangeWorkoutCount", "countBucket", geometry
        FROM features WHERE source_kind = 'path' AND NOT focused AND geometry IS NOT NULL) rows), ''::bytea) ||
        COALESCE((SELECT ST_AsMVT(rows, 'coverage_parks', 4096, 'geometry') FROM (
        SELECT "entityId", "entityKind", "sourceEntityId", "sourceEntityKind", "broadClass", name, "localityName", "regionId",
            "rangeWorkoutCount", "countBucket", geometry
        FROM features WHERE source_kind = 'park' AND NOT focused AND geometry IS NOT NULL) rows), ''::bytea) ||
        COALESCE((SELECT ST_AsMVT(rows, 'coverage_focus', 4096, 'geometry') FROM (
        SELECT "entityId", "entityKind", "sourceEntityId", "sourceEntityKind", "broadClass", name, "localityName", "regionId",
            "rangeWorkoutCount", "countBucket", geometry
        FROM features WHERE source_kind = 'path' AND focused AND geometry IS NOT NULL) rows), ''::bytea) ||
        COALESCE((SELECT ST_AsMVT(rows, 'coverage_parks_focus', 4096, 'geometry') FROM (
        SELECT "entityId", "entityKind", "sourceEntityId", "sourceEntityKind", "broadClass", name, "localityName", "regionId",
            "rangeWorkoutCount", "countBucket", geometry
        FROM features WHERE source_kind = 'park' AND focused AND geometry IS NOT NULL) rows), ''::bytea) INTO tile;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.map_selection_coverage_focus(
    target_account_id uuid,
    target_session_id uuid,
    target_selection_id uuid,
    target_generation bigint,
    target_workout_id uuid
)
RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app, public
SET jit = off
AS $function$
DECLARE
    display_context_id uuid;
    result jsonb;
BEGIN
    SELECT context.id INTO display_context_id
    FROM app.map_selections selection
    JOIN app.map_selection_workouts selected ON selected.account_id = selection.account_id
        AND selected.selection_id = selection.id AND selected.workout_id = target_workout_id
    JOIN app.sessions session_row ON session_row.id = selection.session_id
    JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
    JOIN app.users owner_row ON owner_row.principal_id = principal.id AND owner_row.account_id = selection.account_id
    JOIN app.accounts account ON account.id = owner_row.account_id
    JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
    JOIN app.coverage_display_contexts context ON context.account_id = selection.account_id
        AND context.id = selection.coverage_display_context_id
    JOIN app.coverage_display_revisions revision ON revision.account_id = selection.account_id
    JOIN app.workout_coverage_states coverage ON coverage.account_id = selection.account_id
        AND coverage.workout_id = target_workout_id AND coverage.applied_route_input_revision IS NOT NULL
    JOIN app.workouts workout ON workout.account_id = selection.account_id AND workout.id = target_workout_id
    JOIN app.workout_routes route ON route.account_id = workout.account_id AND route.workout_id = workout.id
    WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
        AND selection.session_id = target_session_id AND selection.generation = target_generation
        AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
        AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
        AND principal.disabled_at IS NULL AND account.state = 'active'
        AND target_account_id = app.current_account_id() AND target_session_id = app.current_session_id()
        AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
        AND workout.local_start_date BETWEEN selection.start_date AND selection.end_date
        AND context.generation = target_generation AND context.revision = revision.revision
        AND context.start_date = selection.start_date AND context.end_date = selection.end_date
        AND context.ready AND context.policy_version = 'coverage-display-identity-v1' AND context.label_locale = 'en';
    IF NOT FOUND THEN RETURN NULL; END IF;

    WITH checked AS MATERIALIZED (
        SELECT match.*, segment.highway, segment.tags FROM app.workout_segment_matches match
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE match.account_id = target_account_id AND match.workout_id = target_workout_id
    ), pieces AS (
        SELECT member.canonical_id, member.source_kind, member.source_id, checked.geom
        FROM checked
        JOIN app.coverage_paths path ON path.account_id = checked.account_id AND path.logical_path_id = checked.logical_path_id
        JOIN app.coverage_display_members member ON member.account_id = target_account_id
            AND member.context_id = display_context_id AND member.source_kind = 'path'
            AND member.source_id = COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id)
        WHERE NOT (checked.highway = 'service' AND COALESCE(checked.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR checked.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (checked.tags ? 'workouts:park_id'))
        UNION ALL
        SELECT member.canonical_id, member.source_kind, member.source_id, checked.geom
        FROM checked
        JOIN app.coverage_display_members member ON member.account_id = target_account_id
            AND member.context_id = display_context_id AND member.source_kind = 'park'
            AND member.source_id = (checked.tags->>'workouts:park_id')::uuid
        WHERE checked.tags ? 'workouts:park_id'
    ), shapes AS (
        SELECT canonical_id, source_kind, source_id, ST_UnaryUnion(ST_Collect(geom)) geom
        FROM pieces GROUP BY canonical_id, source_kind, source_id
    ), features AS (
        SELECT entity.canonical_id, entity.entity_kind, entity.broad_class, entity.name, entity.locality_name, entity.region_id,
            entity.range_workout_count, shapes.source_kind, shapes.source_id, shapes.geom
        FROM shapes JOIN app.coverage_display_entities entity ON entity.account_id = target_account_id
            AND entity.context_id = display_context_id AND entity.canonical_id = shapes.canonical_id
        WHERE entity.range_workout_count > 0
    )
    SELECT jsonb_build_object('type', 'FeatureCollection', 'features', COALESCE(jsonb_agg(jsonb_build_object(
        'type', 'Feature', 'properties', jsonb_build_object(
            'entityId', upper(replace(canonical_id::text, '-', '')), 'entityKind', entity_kind,
            'sourceEntityId', upper(replace(source_id::text, '-', '')), 'sourceEntityKind', source_kind,
            'broadClass', broad_class, 'name', name, 'localityName', locality_name, 'regionId', region_id,
            'rangeWorkoutCount', range_workout_count, 'countBucket', app.coverage_count_bucket(range_workout_count)),
        'geometry', ST_AsGeoJSON(geom, 6)::jsonb)
        ORDER BY canonical_id, source_kind, source_id), '[]'::jsonb)) INTO result FROM features WHERE geom IS NOT NULL;
    RETURN result;
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_entity_detail(uuid, uuid, uuid, bigint, text, uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_mvt(integer, integer, integer, json) OWNER TO workouts_security_owner;
ALTER FUNCTION app.map_selection_coverage_focus(uuid, uuid, uuid, bigint, uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

-- Publish precomputations and canonical readers as one runtime contract.
UPDATE app.schema_metadata SET schema_version = 30, minimum_runtime_version = 30 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'coverage display is forward-only: evidence precomputations and canonical context bindings cannot be undone safely; restore the pre-migration backup to roll back'
        USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
