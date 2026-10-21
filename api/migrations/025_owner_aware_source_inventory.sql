-- +goose Up
ALTER TABLE app.source_objects
ADD COLUMN ownership_blocked_epoch bigint CHECK (ownership_blocked_epoch >= 0);

CREATE INDEX workout_import_events_ownership_blocked_file_idx
ON app.workout_import_events (account_id, job_id, source_file_id)
WHERE kind = 'skipped_live_owner';

-- The successful bytes remain valid; only ownership eligibility becomes stale.
-- +goose StatementBegin
CREATE FUNCTION app.advance_workout_ownership_epoch () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    PERFORM 1 FROM app.accounts account WHERE account.id = NEW.account_id
        AND account.id = app.current_account_id() FOR NO KEY UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid workout ownership account' USING ERRCODE = '42501'; END IF;
    UPDATE app.accounts account SET workout_ownership_epoch = account.workout_ownership_epoch + 1
     WHERE account.id = NEW.account_id;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS sources_workout_ownership_after_delete ON app.sources;
CREATE TRIGGER sources_ownership_epoch_after_delete
AFTER UPDATE OF deleted_at ON app.sources FOR EACH ROW
WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
EXECUTE FUNCTION app.advance_workout_ownership_epoch ();

GRANT UPDATE (workout_ownership_epoch) ON app.accounts TO workouts_security_owner;

-- Called in the file's write transaction, after all import events are inserted.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.record_successful_source_object (
    target_job_id uuid,
    claiming_worker text,
    current_lease_token uuid,
    object_name text,
    object_export_date date,
    object_size bigint,
    object_modified_at timestamptz,
    object_identity text,
    object_checksum bytea
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE
    target_account uuid;
    target_source uuid;
    captured_epoch bigint;
    blocked_epoch bigint;
    first_seen boolean;
BEGIN
    IF claiming_worker IS NULL OR claiming_worker = '' OR current_lease_token IS NULL OR
       object_name IS NULL OR object_name = '' OR object_size IS NULL OR object_size < 0 OR
       object_identity IS NULL OR length(object_identity) NOT BETWEEN 1 AND 512 OR
       object_checksum IS NULL OR octet_length(object_checksum) <> 32 THEN
        RAISE EXCEPTION 'invalid source object arguments' USING ERRCODE = '22023';
    END IF;
    SELECT capability.account_id, capability.source_id, capability.ownership_epoch
      INTO target_account, target_source, captured_epoch
      FROM app.ingest_write_capabilities capability
      JOIN app.accounts account ON account.id = capability.account_id AND account.state = 'active'
      JOIN app.jobs child ON child.id = capability.job_id AND child.account_id = capability.account_id
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
       AND capability.job_id = target_job_id AND capability.worker_id = claiming_worker
       AND capability.lease_token = current_lease_token AND capability.account_id = app.current_account_id()
       AND capability.runtime_version >= 26
       AND capability.runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
       AND child.status = 'running' AND child.cancel_requested_at IS NULL
        AND child.worker_id = claiming_worker AND child.lease_token = current_lease_token;
    IF NOT FOUND OR captured_epoch IS NULL THEN RETURN false; END IF;
    -- A valid transaction fence holds the source/parent/child locks through
    -- commit, even when processing outlasts the lease's wall-clock deadline.
    -- Never read the current account epoch here: deletion may have committed
    -- after a live-owner skip but before this inventory write.
    IF EXISTS (
        SELECT 1 FROM app.workout_import_events event
        JOIN app.source_files file ON file.id = event.source_file_id AND file.account_id = event.account_id
         WHERE event.account_id = target_account AND event.source_id = target_source
           AND event.job_id = target_job_id AND event.kind = 'skipped_live_owner'
           AND file.source_id = target_source AND file.job_id = target_job_id AND file.relative_name = object_name
    ) THEN
        blocked_epoch := captured_epoch;
    END IF;
    PERFORM 1 FROM app.source_sync_state WHERE account_id = target_account AND source_id = target_source FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT NOT EXISTS (SELECT 1 FROM app.source_objects
        WHERE account_id = target_account AND source_id = target_source AND relative_name = object_name) INTO first_seen;
    INSERT INTO app.source_objects(account_id, source_id, relative_name, export_date, observed_size, observed_modified_at,
        observed_identity, successful_checksum, successful_observed_at, successful_job_id, ownership_blocked_epoch)
    VALUES(target_account, target_source, object_name, object_export_date, object_size, object_modified_at, object_identity,
        object_checksum, transaction_timestamp(), target_job_id, blocked_epoch)
    ON CONFLICT(account_id, source_id, relative_name) DO UPDATE SET export_date = EXCLUDED.export_date,
        observed_size = EXCLUDED.observed_size, observed_modified_at = EXCLUDED.observed_modified_at,
        observed_identity = EXCLUDED.observed_identity, successful_checksum = EXCLUDED.successful_checksum,
        successful_observed_at = EXCLUDED.successful_observed_at, successful_job_id = EXCLUDED.successful_job_id,
        ownership_blocked_epoch = EXCLUDED.ownership_blocked_epoch, updated_at = transaction_timestamp();
    IF first_seen THEN
        INSERT INTO app.source_sync_state(account_id, source_id, last_new_export_discovered_at, last_new_export_date, stale_since)
        VALUES(target_account, target_source, transaction_timestamp(), object_export_date, NULL)
        ON CONFLICT(account_id, source_id) DO UPDATE SET
            last_new_export_discovered_at = GREATEST(app.source_sync_state.last_new_export_discovered_at, transaction_timestamp()),
            last_new_export_date = CASE
                WHEN app.source_sync_state.last_new_export_date IS NULL THEN EXCLUDED.last_new_export_date
                WHEN EXCLUDED.last_new_export_date IS NULL THEN app.source_sync_state.last_new_export_date
                ELSE GREATEST(app.source_sync_state.last_new_export_date, EXCLUDED.last_new_export_date) END,
            stale_since = NULL;
        UPDATE app.notifications SET state = 'resolved', resolved_at = transaction_timestamp(), remind_at = NULL, updated_at = transaction_timestamp()
         WHERE account_id = target_account AND type = 'source-stale' AND condition_key = 'source:' || target_source::text
           AND state IN ('unresolved', 'remind');
    END IF;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- Uses exactly the manifest JSON contract. Restoring actions never substitutes
-- for record_ingest_file_manifest's bidirectional metadata/set comparison.
-- +goose StatementBegin
CREATE FUNCTION app.read_ingest_candidate_actions (
    job uuid, worker text, lease uuid, candidates jsonb
) RETURNS TABLE (relative_name text, action text) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE
    target_source uuid;
    target_account uuid := app.current_account_id();
    current_epoch bigint;
    ingest_mode text;
    frozen boolean;
    item jsonb;
BEGIN
    IF jsonb_typeof(candidates) IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;
    IF jsonb_array_length(candidates) > 10000 THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;
    FOR item IN SELECT value FROM jsonb_array_elements(candidates) LOOP
        IF jsonb_typeof(item) IS DISTINCT FROM 'object' THEN
            RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
        END IF;
        IF NOT item ?& ARRAY['name','export_date','size','modified_sec','modified_ns','device','inode','ctime_sec','ctime_ns','action'] OR
           EXISTS (SELECT 1 FROM jsonb_object_keys(item) key WHERE key NOT IN
               ('name','export_date','size','modified_sec','modified_ns','device','inode','ctime_sec','ctime_ns','action','observed_identity')) OR
           (SELECT count(*) FROM jsonb_object_keys(item)) <> 10 + (item ? 'observed_identity')::integer OR
           jsonb_typeof(item->'name') IS DISTINCT FROM 'string' OR item->>'name' !~ '^HealthAutoExport-[0-9]{4}-[0-9]{2}-[0-9]{2}\.json$' OR
           jsonb_typeof(item->'export_date') IS DISTINCT FROM 'string' OR item->>'export_date' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR
           jsonb_typeof(item->'action') IS DISTINCT FROM 'string' OR item->>'action' NOT IN ('process','skip') OR
           EXISTS (SELECT 1 FROM unnest(ARRAY['size','modified_sec','modified_ns','device','inode','ctime_sec','ctime_ns']) field
               WHERE jsonb_typeof(item->field) IS DISTINCT FROM 'number' OR
                   item->>field !~ CASE WHEN field IN ('modified_sec','ctime_sec') THEN '^-?[0-9]+$' ELSE '^[0-9]+$' END) OR
           (item ? 'observed_identity' AND (jsonb_typeof(item->'observed_identity') IS DISTINCT FROM 'string' OR
               length(item->>'observed_identity') > 512 OR item->>'observed_identity' !~ '^v2:rclone:[0-9a-f]{64}$')) THEN
            RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
        END IF;
        IF (item->>'modified_ns')::numeric NOT BETWEEN 0 AND 999999999 OR
           (item->>'ctime_ns')::numeric NOT BETWEEN 0 AND 999999999 OR
           (item->>'size')::numeric > 9223372036854775807 OR
           (item->>'modified_sec')::numeric NOT BETWEEN -9223372036854775808 AND 9223372036854775807 OR
           (item->>'ctime_sec')::numeric NOT BETWEEN -9223372036854775808 AND 9223372036854775807 OR
           (item->>'device')::numeric > 18446744073709551615 OR (item->>'inode')::numeric > 18446744073709551615 OR
           to_char((item->>'export_date')::date, 'YYYY-MM-DD') <> item->>'export_date' THEN
            RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
        END IF;
    END LOOP;
    IF (SELECT count(DISTINCT value->>'name') FROM jsonb_array_elements(candidates) value) <> jsonb_array_length(candidates) THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;

    SELECT fence.source_id INTO target_source FROM app.fence_ingest_job(job, worker, lease, 26) fence;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT account.workout_ownership_epoch INTO current_epoch FROM app.accounts account
     WHERE account.id = target_account AND account.state = 'active';
    IF NOT FOUND THEN RETURN; END IF;
    SELECT j.parameters->>'mode' INTO ingest_mode FROM app.jobs j WHERE j.id = job AND j.account_id = target_account;
    SELECT EXISTS (SELECT 1 FROM app.job_file_candidate_sets s WHERE s.job_id = job AND s.account_id = target_account) INTO frozen;

    RETURN QUERY
    SELECT candidate.name,
        CASE
            WHEN frozen THEN COALESCE(stored.action, 'process')
            WHEN ingest_mode IS DISTINCT FROM 'incremental' THEN 'process'
            WHEN EXISTS (SELECT 1 FROM app.source_files file WHERE file.job_id = job AND file.account_id = target_account
                AND file.relative_name = candidate.name AND file.state IN ('succeeded','failed')) THEN 'process'
            WHEN inventory.successful_checksum IS NOT NULL
                AND inventory.observed_identity = COALESCE(candidate.observed_identity,
                    format('v1:%s:%s:%s:%s:%s:%s:%s', candidate.size, candidate.modified_sec, candidate.modified_ns,
                        candidate.device, candidate.inode, candidate.ctime_sec, candidate.ctime_ns))
                AND (inventory.ownership_blocked_epoch IS NULL OR inventory.ownership_blocked_epoch = current_epoch) THEN 'skip'
            ELSE 'process'
        END
    FROM jsonb_to_recordset(candidates) AS candidate(name text, size bigint, modified_sec bigint, modified_ns integer,
        device numeric, inode numeric, ctime_sec bigint, ctime_ns integer, observed_identity text)
    LEFT JOIN app.job_file_candidates stored ON frozen AND stored.job_id = job AND stored.account_id = target_account
        AND stored.relative_name = candidate.name
    LEFT JOIN app.source_objects inventory ON inventory.account_id = target_account AND inventory.source_id = target_source
        AND inventory.relative_name = candidate.name
    ORDER BY candidate.name COLLATE "C";
END;
$function$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.advance_workout_ownership_epoch (),
    app.read_ingest_candidate_actions (uuid, text, uuid, jsonb)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.advance_workout_ownership_epoch () OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_ingest_candidate_actions (uuid, text, uuid, jsonb) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.read_ingest_candidate_actions (uuid, text, uuid, jsonb) TO workouts_worker;

-- Migration 026 publishes the complete runtime-26 write contract.
UPDATE app.schema_metadata SET schema_version = 25, minimum_runtime_version = 25 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'owner-aware source inventory is forward-only' USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
