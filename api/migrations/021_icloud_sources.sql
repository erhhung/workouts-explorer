-- +goose Up
ALTER TABLE app.sources DROP CONSTRAINT sources_type_check;
ALTER TABLE app.sources ADD CONSTRAINT sources_type_check
    CHECK (type IN ('health-auto-export-local', 'health-auto-export-icloud'));

ALTER TABLE app.job_file_candidates ADD COLUMN observed_identity text
    CHECK (observed_identity IS NULL OR
        (length(observed_identity) <= 512 AND observed_identity ~ '^v2:rclone:[0-9a-f]{64}$'));

-- Existing leases deliberately remain unversioned. An already-running old
-- scheduler must not enqueue failed children for a provider it cannot decode.
ALTER TABLE app.account_sync_schedules ADD COLUMN lease_runtime_version integer
    CHECK (lease_runtime_version IS NULL OR lease_runtime_version > 0);
GRANT SELECT ON app.schema_metadata TO workouts_security_owner;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_worker_job(claiming_worker text, new_lease_token uuid, lease_duration interval, runtime_version integer)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM app.schema_metadata metadata WHERE metadata.singleton
        AND runtime_version >= 21 AND runtime_version >= metadata.minimum_runtime_version) THEN RETURN; END IF;
    RETURN QUERY SELECT claimed.job_id, claimed.account_id, claimed.kind
      FROM app.claim_next_worker_job_internal(claiming_worker, new_lease_token, lease_duration, true) claimed;
END;
$function$;
-- +goose StatementEnd

-- The other versionless worker claim already rejects all callers. This legacy
-- check-only entry point must also stop claiming now that checks can be remote.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_source_connection_check(claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS TABLE(job_id uuid, account_id uuid)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    RETURN;
END;
$function$;
-- +goose StatementEnd

-- Retain the established scheduler implementations behind private wrappers,
-- including their source-set validation, coalescing and failure notifications.
ALTER FUNCTION app.claim_due_sync_account(text,uuid,interval,integer) RENAME TO claim_due_sync_account_internal;
ALTER FUNCTION app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb) RENAME TO enqueue_leased_scheduled_ingest_internal;
ALTER FUNCTION app.finish_sync_account(uuid,text,uuid,uuid) RENAME TO finish_sync_account_internal;
REVOKE ALL ON FUNCTION app.claim_due_sync_account_internal(text,uuid,interval,integer),
    app.enqueue_leased_scheduled_ingest_internal(uuid,text,uuid,uuid,bytea,jsonb),
    app.finish_sync_account_internal(uuid,text,uuid,uuid)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

-- +goose StatementBegin
CREATE FUNCTION app.claim_due_sync_account(claiming_worker text, new_lease_token uuid, lease_duration interval, runtime_version integer)
RETURNS TABLE(account_id uuid, lease_token uuid, next_run_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE claimed record;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM app.schema_metadata metadata WHERE metadata.singleton
        AND runtime_version >= 21 AND runtime_version >= metadata.minimum_runtime_version) THEN RETURN; END IF;
    SELECT * INTO claimed FROM app.claim_due_sync_account_internal(claiming_worker, new_lease_token, lease_duration, runtime_version);
    IF NOT FOUND THEN RETURN; END IF;
    RETURN QUERY
    UPDATE app.account_sync_schedules schedule SET lease_runtime_version = runtime_version
      WHERE schedule.account_id = claimed.account_id
        AND schedule.lease_worker = claiming_worker AND schedule.lease_token = new_lease_token
      RETURNING schedule.account_id, schedule.lease_token, schedule.next_run_at;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_leased_sync_sources(target_account_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid, generation bigint, config_envelope bytea, display_name text, source_type text)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT source.id, source.generation, source.config_envelope, source.display_name, source.type
      FROM app.account_sync_schedules schedule
      JOIN app.accounts account ON account.id = schedule.account_id
      JOIN app.sources source ON source.account_id = schedule.account_id
     WHERE schedule.account_id = target_account_id AND schedule.lease_worker = claiming_worker
       AND schedule.lease_token = current_lease_token AND schedule.lease_expires_at >= clock_timestamp()
       AND schedule.lease_runtime_version >= 21
       AND schedule.lease_runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
       AND account.state = 'active'
       AND source.deleted_at IS NULL AND source.status = 'connected' AND source.auto_sync_enabled
     ORDER BY source.id
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.enqueue_leased_scheduled_ingest(target_account_id uuid, claiming_worker text, current_lease_token uuid,
    parent_id uuid, input_coalescing_key bytea, children jsonb)
RETURNS TABLE(job_id uuid, reused boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    -- Match the delegated enqueue's account -> schedule -> source lock order.
    PERFORM 1 FROM app.accounts account WHERE account.id = target_account_id AND account.state = 'active' FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    PERFORM 1 FROM app.account_sync_schedules schedule WHERE schedule.account_id = target_account_id
        AND schedule.lease_worker = claiming_worker AND schedule.lease_token = current_lease_token
        AND schedule.lease_expires_at >= clock_timestamp() AND schedule.lease_runtime_version >= 21
        AND schedule.lease_runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    RETURN QUERY SELECT enqueued.job_id, enqueued.reused
      FROM app.enqueue_leased_scheduled_ingest_internal(target_account_id, claiming_worker, current_lease_token,
          parent_id, input_coalescing_key, children) enqueued;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.finish_sync_account(target_account_id uuid, claiming_worker text, current_lease_token uuid, enqueued_job_id uuid)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    PERFORM 1 FROM app.account_sync_schedules schedule WHERE schedule.account_id = target_account_id
        AND schedule.lease_worker = claiming_worker AND schedule.lease_token = current_lease_token
        AND schedule.lease_expires_at >= clock_timestamp() AND schedule.lease_runtime_version >= 21
        AND schedule.lease_runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    IF NOT app.finish_sync_account_internal(target_account_id, claiming_worker, current_lease_token, enqueued_job_id) THEN RETURN false; END IF;
    UPDATE app.account_sync_schedules schedule SET lease_runtime_version = NULL WHERE schedule.account_id = target_account_id;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- The database can validate structure, not AEAD/AAD or which plaintext fields changed.
-- Those checks remain the trusted adapter's responsibility. JSONB has already
-- rejected trailing JSON but cannot detect duplicate keys in the original input.
-- +goose StatementBegin
CREATE FUNCTION app.valid_source_config_envelope(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE SET search_path = pg_catalog AS $function$
DECLARE field text; encoded text; decoded bytea;
BEGIN
    IF jsonb_typeof(value) IS DISTINCT FROM 'object' OR
       octet_length(value::text) > 131072 THEN RETURN false; END IF;
    IF (SELECT count(*) FROM jsonb_object_keys(value)) <> 7 OR
       NOT value ?& ARRAY['version','algorithm','keyId','wrappedKeyNonce','wrappedKey','payloadNonce','ciphertext'] OR
       value->'version' IS DISTINCT FROM '1'::jsonb OR
       value->>'algorithm' IS DISTINCT FROM 'AES-256-GCM+AES-256-GCM' OR
       jsonb_typeof(value->'keyId') IS DISTINCT FROM 'string' OR
       value->>'keyId' !~ '^[a-z0-9][a-z0-9._-]{0,63}$' THEN RETURN false; END IF;
    FOREACH field IN ARRAY ARRAY['wrappedKeyNonce','wrappedKey','payloadNonce','ciphertext'] LOOP
        IF jsonb_typeof(value->field) IS DISTINCT FROM 'string' THEN RETURN false; END IF;
        encoded := value->>field;
        IF encoded !~ '^[A-Za-z0-9_-]+$' OR length(encoded) % 4 = 1 THEN RETURN false; END IF;
        decoded := decode(translate(encoded, '-_', '+/') || repeat('=', (4 - length(encoded) % 4) % 4), 'base64');
        IF translate(replace(rtrim(encode(decoded, 'base64'), '='), E'\n', ''), '+/', '-_') <> encoded OR
           (field IN ('wrappedKeyNonce','payloadNonce') AND octet_length(decoded) <> 12) OR
           (field = 'wrappedKey' AND octet_length(decoded) <> 48) OR
           (field = 'ciphertext' AND octet_length(decoded) NOT BETWEEN 16 AND 65536) THEN RETURN false; END IF;
    END LOOP;
    RETURN true;
EXCEPTION WHEN invalid_parameter_value THEN RETURN false;
END;
$function$;
-- +goose StatementEnd

-- Keep the legacy bytea reader for already queued jobs and existing callers.
-- Type comes from the immutable source, never the public log/read-model context.
-- +goose StatementBegin
CREATE FUNCTION app.read_typed_job_source_snapshot(target_job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid, source_generation bigint, source_type text, config_envelope jsonb)
LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT snapshot.source_id, snapshot.source_generation, source.type,
           convert_from(snapshot.config_envelope, 'UTF8')::jsonb
      FROM app.read_job_config_snapshot(target_job_id, claiming_worker, current_lease_token) snapshot
      JOIN app.sources source ON source.id = snapshot.source_id AND source.account_id = app.current_account_id()
      JOIN app.jobs job ON job.id = target_job_id AND job.account_id = source.account_id
     WHERE job.lease_expires_at >= clock_timestamp()
$function$;
-- +goose StatementEnd

-- Shared only by fixed definer functions. Locks follow account -> source ->
-- parent -> child, including tombstones so cancelled checks can finish cleanup.
-- +goose StatementBegin
CREATE FUNCTION app.lock_source_job_for_credentials(target_job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid, snapshot_generation bigint, current_generation bigint,
    source_status text, source_deleted_at timestamptz, job_kind text, cancellation_pending boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE target_source uuid; captured_generation bigint; target_parent uuid;
BEGIN
    PERFORM 1 FROM app.accounts account WHERE account.id = app.current_account_id()
        AND account.state = 'active' FOR SHARE;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT snapshot.source_id, snapshot.source_generation, job.parent_job_id
      INTO target_source, captured_generation, target_parent
      FROM app.job_config_snapshots snapshot
      JOIN app.jobs job ON job.id = snapshot.job_id AND job.account_id = snapshot.account_id
     WHERE snapshot.job_id = target_job_id AND snapshot.account_id = app.current_account_id()
       AND job.kind IN ('source_connection_check','manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN; END IF;
    SELECT source.generation, source.status, source.deleted_at
      INTO current_generation, source_status, source_deleted_at
      FROM app.sources source WHERE source.id = target_source AND source.account_id = app.current_account_id() FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    cancellation_pending := false;
    IF target_parent IS NOT NULL THEN
        SELECT parent.cancel_requested_at IS NOT NULL INTO cancellation_pending
          FROM app.jobs parent WHERE parent.id = target_parent AND parent.account_id = app.current_account_id()
            AND parent.kind IN ('manual_ingest','scheduled_ingest') AND parent.status = 'running' FOR UPDATE;
        IF NOT FOUND THEN RETURN; END IF;
    END IF;
    SELECT job.kind, cancellation_pending OR job.cancel_requested_at IS NOT NULL
      INTO job_kind, cancellation_pending
      FROM app.jobs job
      JOIN app.job_config_snapshots snapshot ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
     WHERE job.id = target_job_id AND job.account_id = app.current_account_id()
       AND snapshot.source_id = target_source AND snapshot.source_generation = captured_generation
       AND job.kind IN ('source_connection_check','manual_ingest_source','scheduled_ingest_source')
       AND job.status = 'running' AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp() FOR UPDATE OF job;
    IF NOT FOUND THEN RETURN; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id = target_job_id AND job.account_id = app.current_account_id()
        AND job.lease_expires_at >= clock_timestamp();
    IF NOT FOUND THEN RETURN; END IF;
    source_id := target_source;
    snapshot_generation := captured_generation;
    RETURN NEXT;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.refresh_source_credentials(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    expected_generation bigint, config_envelope jsonb) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE fence record;
BEGIN
    SELECT * INTO fence FROM app.lock_source_job_for_credentials(target_job_id, claiming_worker, current_lease_token);
    IF NOT FOUND OR fence.job_kind NOT IN ('manual_ingest_source','scheduled_ingest_source') OR
       fence.cancellation_pending OR fence.source_deleted_at IS NOT NULL OR
       fence.source_status NOT IN ('checking-connection','connected') OR
       expected_generation IS DISTINCT FROM fence.snapshot_generation OR
       expected_generation IS DISTINCT FROM fence.current_generation THEN RETURN false; END IF;
    IF NOT app.valid_source_config_envelope(config_envelope) THEN
        RAISE EXCEPTION 'invalid source configuration envelope' USING ERRCODE = '22023';
    END IF;
    UPDATE app.sources source SET config_envelope = convert_to(refresh_source_credentials.config_envelope::text, 'UTF8'), generation = source.generation + 1
     WHERE source.id = fence.source_id AND source.account_id = app.current_account_id();
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- Completion publishes config and status against the OLD snapshot generation,
-- then deletes the snapshot only through the existing terminal transition.
-- +goose StatementBegin
CREATE FUNCTION app.finish_connection_check_with_refresh(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    result_status text, status_code_value text, status_summary_value text, config_envelope jsonb) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE fence record; terminal_status text; finished boolean;
BEGIN
    IF result_status IS NULL OR result_status NOT IN ('connected','connection-failed') OR
       (result_status = 'connected' AND (status_code_value IS NOT NULL OR status_summary_value IS NOT NULL)) OR
       (result_status = 'connection-failed' AND
        (status_code_value IS NULL OR length(status_code_value) NOT BETWEEN 1 AND 64 OR
         status_summary_value IS NULL OR length(status_summary_value) NOT BETWEEN 1 AND 512 OR config_envelope IS NOT NULL)) THEN
        RAISE EXCEPTION 'invalid source connection result' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO fence FROM app.lock_source_job_for_credentials(target_job_id, claiming_worker, current_lease_token);
    IF NOT FOUND OR fence.job_kind <> 'source_connection_check' THEN RETURN false; END IF;
    -- The exception block rolls back all writes if the terminal lease fence
    -- expires during completion; the caller still receives false.
    IF NOT fence.cancellation_pending AND fence.source_deleted_at IS NULL AND
       fence.current_generation = fence.snapshot_generation AND fence.source_status = 'checking-connection' THEN
        IF config_envelope IS NOT NULL AND NOT app.valid_source_config_envelope(config_envelope) THEN
            RAISE EXCEPTION 'invalid source configuration envelope' USING ERRCODE = '22023';
        END IF;
        UPDATE app.sources source SET status = result_status, status_code = status_code_value,
            status_summary = status_summary_value, checked_at = transaction_timestamp(),
            config_envelope = CASE WHEN finish_connection_check_with_refresh.config_envelope IS NULL THEN source.config_envelope
                ELSE convert_to(finish_connection_check_with_refresh.config_envelope::text, 'UTF8') END,
            generation = source.generation + CASE WHEN finish_connection_check_with_refresh.config_envelope IS NULL THEN 0 ELSE 1 END
         WHERE source.id = fence.source_id AND source.account_id = app.current_account_id();
        IF result_status = 'connected' THEN
            UPDATE app.notifications SET state = 'resolved', resolved_at = transaction_timestamp(), remind_at = NULL,
                updated_at = transaction_timestamp()
             WHERE account_id = app.current_account_id() AND type = 'source-connection-failed'
               AND condition_key = 'source:' || fence.source_id::text AND state IN ('unresolved','remind');
        ELSE
            INSERT INTO app.notifications(id, account_id, type, severity, condition_key, subject_type, subject_id, source_id, job_id, title, message)
            VALUES(gen_random_uuid(), app.current_account_id(), 'source-connection-failed', 'error', 'source:' || fence.source_id::text,
                'source', fence.source_id, fence.source_id, target_job_id, 'Source connection failed', 'The source connection could not be verified.')
            ON CONFLICT(account_id, type, condition_key) DO UPDATE SET state = 'unresolved', resolved_at = NULL, remind_at = NULL,
                job_id = EXCLUDED.job_id, message = EXCLUDED.message, updated_at = transaction_timestamp();
        END IF;
    END IF;
    terminal_status := CASE WHEN fence.cancellation_pending THEN 'cancelled' WHEN result_status = 'connected' THEN 'succeeded' ELSE 'failed' END;
    finished := app.finish_job(target_job_id, claiming_worker, current_lease_token, terminal_status,
        CASE WHEN terminal_status = 'failed' THEN status_code_value END,
        CASE WHEN terminal_status = 'failed' THEN status_summary_value END);
    IF NOT finished THEN
        RAISE EXCEPTION 'source connection completion fence expired' USING ERRCODE = 'P0002';
    END IF;
    RETURN true;
EXCEPTION WHEN no_data_found THEN RETURN false;
END;
$function$;
-- +goose StatementEnd

-- An ingest auth failure changes eligibility, not generation or the live snapshot.
-- Ignore caller summaries entirely: provider errors can contain secrets or paths.
-- +goose StatementBegin
CREATE FUNCTION app.fail_source_connection(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    expected_generation bigint, status_code_value text, status_summary_value text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE fence record; safe_summary text;
BEGIN
    safe_summary := CASE status_code_value
        WHEN 'source-auth-expired' THEN 'Source authentication expired. Reconnect the source.'
        WHEN 'source-auth-failed' THEN 'Source authentication failed. Reconnect the source.'
        WHEN 'source-adp-approval-required' THEN 'Approve iCloud access on a trusted Apple device, then reconnect the source.' END;
    IF safe_summary IS NULL THEN RAISE EXCEPTION 'invalid source authentication failure code' USING ERRCODE = '22023'; END IF;
    SELECT * INTO fence FROM app.lock_source_job_for_credentials(target_job_id, claiming_worker, current_lease_token);
    IF NOT FOUND OR fence.job_kind NOT IN ('manual_ingest_source','scheduled_ingest_source') OR
       fence.cancellation_pending OR fence.source_deleted_at IS NOT NULL OR fence.source_status <> 'connected' OR
       expected_generation IS DISTINCT FROM fence.snapshot_generation OR
       expected_generation IS DISTINCT FROM fence.current_generation THEN RETURN false; END IF;
    UPDATE app.sources source SET status = 'connection-failed', status_code = status_code_value,
        status_summary = safe_summary, checked_at = transaction_timestamp()
     WHERE source.id = fence.source_id AND source.account_id = app.current_account_id();
    INSERT INTO app.notifications(id, account_id, type, severity, condition_key, subject_type, subject_id, source_id, job_id, title, message)
    VALUES(gen_random_uuid(), app.current_account_id(), 'source-connection-failed', 'error', 'source:' || fence.source_id::text,
        'source', fence.source_id, fence.source_id, target_job_id, 'Source connection failed', safe_summary)
    ON CONFLICT(account_id, type, condition_key) DO UPDATE SET state = 'unresolved', resolved_at = NULL, remind_at = NULL,
        job_id = EXCLUDED.job_id, message = EXCLUDED.message, updated_at = transaction_timestamp();
    PERFORM app.evaluate_source_staleness(fence.source_id);
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- Local manifests retain exactly their ten fields. Remote identity is an
-- optional eleventh field and participates in BOTH directions of exact equality.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.record_ingest_file_manifest(target_job_id uuid, claiming_worker text, current_lease_token uuid, candidate_manifest jsonb)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE target_account uuid; target_parent uuid; target_source uuid; manifest_count integer; stored_count integer; item jsonb;
BEGIN
    IF claiming_worker IS NULL OR claiming_worker = '' OR current_lease_token IS NULL OR
       jsonb_typeof(candidate_manifest) IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;
    manifest_count := jsonb_array_length(candidate_manifest);
    IF manifest_count > 10000 THEN RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023'; END IF;
    FOR item IN SELECT value FROM jsonb_array_elements(candidate_manifest) LOOP
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
    IF (SELECT count(DISTINCT value->>'name') FROM jsonb_array_elements(candidate_manifest) value) <> manifest_count THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;
    SELECT job.account_id, job.parent_job_id, context.source_id INTO target_account, target_parent, target_source
      FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
     WHERE job.id = target_job_id AND job.account_id = app.current_account_id()
       AND job.kind IN ('manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.sources source WHERE source.id = target_source AND source.account_id = target_account
        AND source.deleted_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id = target_parent AND parent.account_id = target_account
        AND parent.status = 'running' AND parent.cancel_requested_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id = target_job_id AND job.account_id = target_account
        AND job.status = 'running' AND job.cancel_requested_at IS NULL AND job.worker_id = claiming_worker
        AND job.lease_token = current_lease_token AND job.lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT candidate_count INTO stored_count FROM app.job_file_candidate_sets WHERE job_id = target_job_id FOR UPDATE;
    IF NOT FOUND THEN
        INSERT INTO app.job_file_candidate_sets(job_id, account_id, source_id, candidate_count)
        VALUES(target_job_id, target_account, target_source, manifest_count);
        INSERT INTO app.job_file_candidates(job_id, account_id, source_id, relative_name, export_date, observed_size,
            modified_sec, modified_ns, device, inode, ctime_sec, ctime_ns, action, observed_identity)
        SELECT target_job_id, target_account, target_source, candidate.name, candidate.export_date, candidate.size,
            candidate.modified_sec, candidate.modified_ns, candidate.device, candidate.inode, candidate.ctime_sec,
            candidate.ctime_ns, candidate.action, candidate.observed_identity
          FROM jsonb_to_recordset(candidate_manifest) AS candidate(name text, export_date date, size bigint,
            modified_sec bigint, modified_ns integer, device numeric, inode numeric, ctime_sec bigint, ctime_ns integer, action text, observed_identity text);
        RETURN 'created';
    END IF;
    IF stored_count <> manifest_count OR EXISTS (
        (SELECT relative_name, export_date, observed_size, modified_sec, modified_ns, device, inode, ctime_sec, ctime_ns, action, observed_identity
           FROM app.job_file_candidates WHERE job_id = target_job_id
         EXCEPT
         SELECT candidate.name, candidate.export_date, candidate.size, candidate.modified_sec, candidate.modified_ns,
             candidate.device, candidate.inode, candidate.ctime_sec, candidate.ctime_ns, candidate.action, candidate.observed_identity
           FROM jsonb_to_recordset(candidate_manifest) AS candidate(name text, export_date date, size bigint,
             modified_sec bigint, modified_ns integer, device numeric, inode numeric, ctime_sec bigint, ctime_ns integer, action text, observed_identity text))
        UNION ALL
        (SELECT candidate.name, candidate.export_date, candidate.size, candidate.modified_sec, candidate.modified_ns,
             candidate.device, candidate.inode, candidate.ctime_sec, candidate.ctime_ns, candidate.action, candidate.observed_identity
           FROM jsonb_to_recordset(candidate_manifest) AS candidate(name text, export_date date, size bigint,
             modified_sec bigint, modified_ns integer, device numeric, inode numeric, ctime_sec bigint, ctime_ns integer, action text, observed_identity text)
         EXCEPT
         SELECT relative_name, export_date, observed_size, modified_sec, modified_ns, device, inode, ctime_sec, ctime_ns, action, observed_identity
           FROM app.job_file_candidates WHERE job_id = target_job_id)
    ) THEN RETURN 'mismatch'; END IF;
    RETURN 'matched';
END;
$function$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.valid_source_config_envelope(jsonb),
    app.lock_source_job_for_credentials(uuid,text,uuid),
    app.read_typed_job_source_snapshot(uuid,text,uuid),
    app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb),
    app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb),
    app.fail_source_connection(uuid,text,uuid,bigint,text,text),
    app.claim_due_sync_account(text,uuid,interval,integer),
    app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb),
    app.finish_sync_account(uuid,text,uuid,uuid)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.valid_source_config_envelope(jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.lock_source_job_for_credentials(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_typed_job_source_snapshot(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.fail_source_connection(uuid,text,uuid,bigint,text,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_due_sync_account(text,uuid,interval,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.finish_sync_account(uuid,text,uuid,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT UPDATE(generation) ON app.sources TO workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.read_typed_job_source_snapshot(uuid,text,uuid),
    app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb),
    app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb),
    app.fail_source_connection(uuid,text,uuid,bigint,text,text),
    app.claim_due_sync_account(text,uuid,interval,integer),
    app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb),
    app.finish_sync_account(uuid,text,uuid,uuid) TO workouts_worker;

-- Older workers cannot safely dispatch iCloud sources or honor remote manifests.
UPDATE app.schema_metadata SET schema_version = 21, minimum_runtime_version = 21;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'schema 21 is forward-only: iCloud source credentials and remote identities cannot be downgraded' USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
