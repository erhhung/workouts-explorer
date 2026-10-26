-- +goose Up
-- Omission in persisted bounded jobs retains the shipped force policy. New API
-- jobs explicitly persist reprocessAllFiles; incremental jobs never carry it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.valid_ingest_child_parameters(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE SET search_path = pg_catalog AS $function$
DECLARE start_value date; end_value date;
BEGIN
    IF jsonb_typeof(value) IS DISTINCT FROM 'object' THEN RETURN false; END IF;
    IF NOT value ?& ARRAY['sourceId','generation','mode'] OR
       jsonb_typeof(value->'sourceId') IS DISTINCT FROM 'string' OR value->>'sourceId' !~ '^[0-9A-F]{32}$' OR
       jsonb_typeof(value->'generation') IS DISTINCT FROM 'number' OR value->>'generation' !~ '^[1-9][0-9]*$' OR
       jsonb_typeof(value->'mode') IS DISTINCT FROM 'string' OR value->>'mode' NOT IN ('incremental','bounded') THEN
        RETURN false;
    END IF;
    IF value->>'mode' = 'incremental' THEN
        RETURN (SELECT count(*) = 3 FROM jsonb_object_keys(value));
    END IF;
    IF NOT value ?& ARRAY['startDate','endDate'] OR
       EXISTS (SELECT 1 FROM jsonb_object_keys(value) key WHERE key NOT IN
           ('sourceId','generation','mode','startDate','endDate','reprocessAllFiles','legacySchema6')) OR
       jsonb_typeof(value->'startDate') IS DISTINCT FROM 'string' OR
       jsonb_typeof(value->'endDate') IS DISTINCT FROM 'string' OR
       value->>'startDate' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR
       value->>'endDate' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR
       (value ? 'reprocessAllFiles' AND jsonb_typeof(value->'reprocessAllFiles') IS DISTINCT FROM 'boolean') OR
       (value ? 'legacySchema6' AND (value ? 'reprocessAllFiles' OR
           value->'legacySchema6' IS DISTINCT FROM 'true'::jsonb OR
           value->>'startDate' <> '0001-01-01' OR value->>'endDate' <> '9999-12-31')) THEN
        RETURN false;
    END IF;
    BEGIN
        start_value := (value->>'startDate')::date;
        end_value := (value->>'endDate')::date;
    EXCEPTION WHEN datetime_field_overflow OR invalid_datetime_format THEN RETURN false;
    END;
    RETURN to_char(start_value,'YYYY-MM-DD') = value->>'startDate' AND
        to_char(end_value,'YYYY-MM-DD') = value->>'endDate' AND start_value <= end_value;
END;
$function$;
-- +goose StatementEnd

ALTER TABLE app.source_files
    ADD COLUMN observed_identity text,
    ADD COLUMN ownership_blocked_epoch bigint CHECK (ownership_blocked_epoch >= 0),
    ADD CONSTRAINT source_files_success_receipt CHECK (
        (observed_identity IS NULL AND ownership_blocked_epoch IS NULL) OR
        (state = 'succeeded' AND observed_identity IS NOT NULL AND length(observed_identity) BETWEEN 1 AND 512));

-- Private, once per discovery, not once per file. Follow only predecessor edges;
-- a newer branch is neither required nor eligible evidence for this branch.
-- +goose StatementBegin
CREATE FUNCTION app.ingest_retry_ancestor_jobs(job uuid, target_account uuid, target_source uuid) RETURNS uuid[]
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE
    cursor_id uuid := job;
    visited uuid[] := ARRAY[]::uuid[];
    parents uuid[] := ARRAY[]::uuid[];
    expected_parent uuid;
    policy jsonb;
    node_policy jsonb;
    node record;
BEGIN
    IF target_account IS DISTINCT FROM app.current_account_id() OR target_source IS NULL THEN
        RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
    END IF;
    LOOP
        IF cursor_id IS NULL OR cursor_id = ANY(visited) OR cardinality(visited) >= 100 THEN
            RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
        END IF;
        SELECT child.id,child.kind,child.parameters,child.retry_of_job_id,child.parent_job_id,
            parent.retry_of_job_id AS parent_retry,parent.parent_job_id AS parent_parent,
            parent.kind AS parent_kind,context.source_id,context.source_generation,context.source_type,
            snapshot.job_id AS snapshot_job,snapshot.source_id AS snapshot_source,snapshot.source_generation AS snapshot_generation
          INTO node FROM app.jobs child
          JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = target_account
          JOIN app.job_source_contexts context ON context.job_id = child.id AND context.account_id = target_account
          LEFT JOIN app.job_config_snapshots snapshot ON snapshot.job_id = child.id AND snapshot.account_id = target_account
         WHERE child.id = cursor_id AND child.account_id = target_account;
        IF NOT FOUND THEN RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023'; END IF;
        IF node.kind NOT IN ('manual_ingest_source','scheduled_ingest_source') OR
           node.parent_kind IS DISTINCT FROM (CASE node.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END) OR
           node.parent_parent IS NOT NULL OR node.parent_job_id = ANY(parents) OR
           (cardinality(visited) > 0 AND node.parent_job_id IS DISTINCT FROM expected_parent) OR
           node.source_id IS DISTINCT FROM target_source OR
           -- Terminal cleanup deletes encrypted snapshots, not durable contexts.
           (cardinality(visited) = 0 AND node.snapshot_job IS NULL) OR
           (node.snapshot_job IS NOT NULL AND (node.snapshot_source IS DISTINCT FROM target_source OR
               node.snapshot_generation IS DISTINCT FROM node.source_generation)) OR
           NOT app.valid_ingest_child_parameters(node.parameters) OR
           node.parameters->>'sourceId' IS DISTINCT FROM upper(replace(target_source::text,'-','')) OR
           node.parameters->>'generation' IS DISTINCT FROM node.source_generation::text OR
           app.workout_provider_namespace(node.source_type) IS DISTINCT FROM
               (SELECT app.workout_provider_namespace(source.type) FROM app.sources source
                WHERE source.id = target_source AND source.account_id = target_account) THEN
            RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
        END IF;
        node_policy := jsonb_build_object('mode',node.parameters->'mode',
            'startDate',node.parameters->'startDate','endDate',node.parameters->'endDate',
            'force',CASE WHEN node.parameters->>'mode' = 'bounded'
                THEN COALESCE(node.parameters->'reprocessAllFiles','true'::jsonb) ELSE 'false'::jsonb END);
        IF cardinality(visited) = 0 THEN policy := node_policy;
        ELSIF node_policy IS DISTINCT FROM policy THEN
            RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
        END IF;
        visited := array_append(visited,node.id);
        parents := array_append(parents,node.parent_job_id);
        IF node.retry_of_job_id IS NULL THEN
            IF node.parent_retry IS NOT NULL THEN
                RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
            END IF;
            RETURN visited[2:cardinality(visited)];
        END IF;
        expected_parent := node.parent_retry;
        cursor_id := node.retry_of_job_id;
    END LOOP;
EXCEPTION WHEN invalid_parameter_value THEN
    RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
END;
$function$;
-- +goose StatementEnd

-- A receipt is stamped after import events, before the existing inventory write.
-- NULL identity remains historical/unknown, not evidence of a clean success.
-- +goose StatementBegin
CREATE FUNCTION app.stamp_ingest_source_file_receipt() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE candidate app.job_file_candidates%ROWTYPE; captured_epoch bigint;
BEGIN
    IF NEW.observed_identity IS NOT NULL OR NEW.ownership_blocked_epoch IS NOT NULL THEN
        RAISE EXCEPTION 'source file receipts are private' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state = 'succeeded' THEN
            RAISE EXCEPTION 'source file success requires a processing transition' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state <> 'processing' OR NEW.state <> 'succeeded' THEN RETURN NEW; END IF;
    SELECT capability.ownership_epoch INTO captured_epoch
      FROM app.ingest_write_capabilities capability
      JOIN app.accounts account ON account.id = capability.account_id AND account.state = 'active'
      JOIN app.jobs child ON child.id = capability.job_id AND child.account_id = capability.account_id
      JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
      JOIN app.sources source ON source.id = capability.source_id AND source.account_id = capability.account_id
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
       AND capability.account_id = NEW.account_id AND capability.account_id = app.current_account_id()
       AND capability.source_id = NEW.source_id AND capability.job_id = NEW.job_id
       AND capability.runtime_version >= 31
       AND capability.runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
       AND child.kind IN ('manual_ingest_source','scheduled_ingest_source')
       AND child.status = 'running' AND child.cancel_requested_at IS NULL
       AND child.worker_id = capability.worker_id AND child.lease_token = capability.lease_token
       AND parent.kind = CASE child.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END
       AND parent.status = 'running' AND parent.cancel_requested_at IS NULL AND source.deleted_at IS NULL;
    IF NOT FOUND OR captured_epoch IS NULL THEN
        RAISE EXCEPTION 'source file receipt requires a transaction fence' USING ERRCODE = '42501';
    END IF;
    SELECT stored.* INTO candidate FROM app.job_file_candidates stored
      JOIN app.job_file_candidate_sets frozen ON frozen.job_id = stored.job_id AND frozen.account_id = stored.account_id
          AND frozen.source_id = stored.source_id
     WHERE stored.job_id = NEW.job_id AND stored.account_id = NEW.account_id
       AND stored.source_id = NEW.source_id AND stored.relative_name = NEW.relative_name
       AND stored.action = 'process' AND stored.observed_size = NEW.size_bytes
       AND stored.export_date = substring(NEW.relative_name FROM '^HealthAutoExport-([0-9]{4}-[0-9]{2}-[0-9]{2})\.json$')::date
       -- pgx encodes timestamps at microsecond precision; identity retains all ns.
       AND extract(epoch FROM NEW.modified_at) = stored.modified_sec::numeric + (stored.modified_ns / 1000)::numeric / 1000000;
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid source file receipt manifest' USING ERRCODE = '22023'; END IF;
    NEW.observed_identity := COALESCE(candidate.observed_identity,
        format('v1:%s:%s:%s:%s:%s:%s:%s',candidate.observed_size,candidate.modified_sec,candidate.modified_ns,
            candidate.device,candidate.inode,candidate.ctime_sec,candidate.ctime_ns));
    IF EXISTS (SELECT 1 FROM app.workout_import_events event
        WHERE event.account_id = NEW.account_id AND event.source_id = NEW.source_id
          AND event.job_id = NEW.job_id AND event.source_file_id = NEW.id AND event.kind = 'skipped_live_owner') THEN
        NEW.ownership_blocked_epoch := captured_epoch;
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

CREATE TRIGGER source_files_receipt_before_write BEFORE INSERT OR UPDATE ON app.source_files
FOR EACH ROW EXECUTE FUNCTION app.stamp_ingest_source_file_receipt();

-- Full replacement retains 027's exact private capability runtime lookup. The
-- four-argument fence (including 028's nonlocking generation check) is untouched.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_ingest_candidate_actions(job uuid, worker text, lease uuid, candidates jsonb)
RETURNS TABLE(relative_name text, action text) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE
    target_source uuid;
    target_account uuid := app.current_account_id();
    current_epoch bigint;
    parameters jsonb;
    effective_force boolean;
    ancestors uuid[];
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

    SELECT fence.source_id INTO target_source FROM app.fence_ingest_job(job, worker, lease, COALESCE((
        SELECT capability.runtime_version FROM app.ingest_write_capabilities capability
        WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
          AND capability.account_id = target_account AND capability.job_id = job
          AND capability.worker_id = worker AND capability.lease_token = lease
    ),26)) fence;
    IF NOT FOUND THEN RETURN; END IF;

    -- Frozen decisions precede even lineage validation. Metadata/set matching is
    -- still enforced by record_ingest_file_manifest; terminal checkpoints resume
    -- through the worker, never by rewriting a frozen process to skip.
    IF EXISTS (SELECT 1 FROM app.job_file_candidate_sets s WHERE s.job_id = job AND s.account_id = target_account) THEN
        RETURN QUERY SELECT candidate->>'name',COALESCE(stored.action,'process')
        FROM jsonb_array_elements(candidates) candidate
        LEFT JOIN app.job_file_candidates stored ON stored.job_id = job AND stored.account_id = target_account
            AND stored.relative_name = candidate->>'name'
        ORDER BY (candidate->>'name') COLLATE "C";
        RETURN;
    END IF;
    ancestors := app.ingest_retry_ancestor_jobs(job,target_account,target_source);
    SELECT j.parameters INTO parameters FROM app.jobs j WHERE j.id = job AND j.account_id = target_account;
    effective_force := parameters->>'mode' = 'bounded' AND COALESCE((parameters->>'reprocessAllFiles')::boolean,true);
    SELECT account.workout_ownership_epoch INTO current_epoch FROM app.accounts account
     WHERE account.id = target_account AND account.state = 'active';
    IF NOT FOUND THEN RETURN; END IF;

    RETURN QUERY
    SELECT candidate.name,CASE
        WHEN EXISTS (SELECT 1 FROM app.source_files own_file WHERE own_file.job_id = job AND own_file.account_id = target_account
            AND own_file.relative_name = candidate.name AND own_file.state IN ('succeeded','failed')) THEN 'process'
        WHEN EXISTS (
            SELECT 1 FROM unnest(ancestors) ancestor(id)
            JOIN app.source_files file ON file.job_id = ancestor.id AND file.account_id = target_account
                AND file.source_id = target_source AND file.relative_name = candidate.name AND file.state = 'succeeded'
                AND file.checksum_sha256 IS NOT NULL AND file.size_bytes = candidate.size
            -- The trusted receipt survives private manifest retention and later
            -- inventory replacement. Historical NULL receipts instead require
            -- the current inventory to prove this exact successful file.
            WHERE candidate.export_date = substring(file.relative_name FROM '^HealthAutoExport-([0-9]{4}-[0-9]{2}-[0-9]{2})\.json$')::date
              AND extract(epoch FROM file.modified_at) = candidate.modified_sec::numeric + (candidate.modified_ns / 1000)::numeric / 1000000
              AND ((file.observed_identity = candidate.identity
                    AND (file.ownership_blocked_epoch IS NULL OR file.ownership_blocked_epoch = current_epoch))
                OR (file.observed_identity IS NULL AND inventory.successful_job_id = file.job_id
                    AND inventory.successful_checksum = file.checksum_sha256 AND inventory.observed_identity = candidate.identity
                    AND inventory.observed_size = candidate.size AND inventory.export_date = candidate.export_date
                    AND inventory.observed_modified_at = file.modified_at
                    AND (inventory.ownership_blocked_epoch IS NULL OR inventory.ownership_blocked_epoch = current_epoch)))
        ) THEN 'skip'
        WHEN effective_force THEN 'process'
        WHEN inventory.successful_checksum IS NOT NULL AND inventory.observed_identity = candidate.identity
            AND (inventory.ownership_blocked_epoch IS NULL OR inventory.ownership_blocked_epoch = current_epoch) THEN 'skip'
        ELSE 'process' END
    FROM (SELECT input.*,COALESCE(input.observed_identity,
        format('v1:%s:%s:%s:%s:%s:%s:%s',input.size,input.modified_sec,input.modified_ns,
            input.device,input.inode,input.ctime_sec,input.ctime_ns)) AS identity
        FROM jsonb_to_recordset(candidates) AS input(name text,export_date date,size bigint,modified_sec bigint,modified_ns integer,
            device numeric,inode numeric,ctime_sec bigint,ctime_ns integer,observed_identity text)) candidate
    LEFT JOIN app.source_objects inventory ON inventory.account_id = target_account AND inventory.source_id = target_source
        AND inventory.relative_name = candidate.name
    ORDER BY candidate.name COLLATE "C";
END;
$function$;
-- +goose StatementEnd

-- Replace 004's table-level read grants: otherwise they expose every new column.
-- Preserve the fifteen pre-existing columns and the read_owned_job_files ABI.
-- Worker INSERT/UPDATE remain table-level and are guarded by the receipt trigger.
REVOKE SELECT ON app.source_files FROM workouts_api, workouts_worker;
GRANT SELECT(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,checksum_sha256,
    state,failure_code,failure_summary,processing_started_at,processed_at,created_at,updated_at)
    ON app.source_files TO workouts_api, workouts_worker;
GRANT SELECT(observed_identity,ownership_blocked_epoch) ON app.source_files TO workouts_security_owner;
-- The existing invoker INSERT trigger calls this pure validator as the API role.
GRANT EXECUTE ON FUNCTION app.valid_ingest_child_parameters(jsonb) TO workouts_security_owner, workouts_api;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.ingest_retry_ancestor_jobs(uuid,uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.stamp_ingest_source_file_receipt() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.ingest_retry_ancestor_jobs(uuid,uuid,uuid),app.stamp_ingest_source_file_receipt()
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration;
REVOKE ALL ON FUNCTION app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.read_ingest_candidate_actions(uuid,text,uuid,jsonb) TO workouts_worker;

UPDATE app.schema_metadata SET schema_version = 31, minimum_runtime_version = 31 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$ BEGIN
    RAISE EXCEPTION 'ingest reprocessing is forward-only' USING ERRCODE = '55000';
END; $block$;
-- +goose StatementEnd
