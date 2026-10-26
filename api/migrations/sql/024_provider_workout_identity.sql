-- +goose Up
-- The complete write contract ships with runtime 26 after migrations 025/026.

ALTER TABLE app.accounts ADD COLUMN workout_ownership_epoch bigint NOT NULL DEFAULT 0
    CHECK (workout_ownership_epoch >= 0);
ALTER TABLE app.ingest_write_capabilities
    ADD COLUMN ownership_epoch bigint,
    ADD COLUMN runtime_version integer;
ALTER TABLE app.workouts ADD COLUMN provider_namespace text COLLATE "C" NOT NULL DEFAULT 'health-auto-export'
    CHECK (provider_namespace = 'health-auto-export');
ALTER TABLE app.workout_deletion_targets ADD COLUMN provider_namespace text COLLATE "C" NOT NULL DEFAULT 'health-auto-export'
    CHECK (provider_namespace = 'health-auto-export');

-- Fail closed without printing provider identifiers or merging historical UUIDs.
ALTER TABLE app.workouts NO FORCE ROW LEVEL SECURITY;
-- +goose StatementBegin
DO $block$
BEGIN
    IF EXISTS (SELECT 1 FROM app.workouts WHERE provider_id IS NOT NULL
        GROUP BY account_id, provider_namespace, provider_id HAVING count(*) > 1) THEN
        RAISE EXCEPTION 'workout identity-conflict: resolve duplicate provider identities before upgrading'
            USING ERRCODE = '23505';
    END IF;
END;
$block$;
-- +goose StatementEnd
CREATE UNIQUE INDEX workouts_account_provider_identity_idx
    ON app.workouts(account_id, provider_namespace, provider_id) WHERE provider_id IS NOT NULL;
ALTER TABLE app.workouts FORCE ROW LEVEL SECURITY;
CREATE INDEX workout_deletion_targets_provider_namespace_idx
    ON app.workout_deletion_targets(account_id, provider_namespace, provider_id) WHERE provider_id IS NOT NULL;

-- Historical source/file/job provenance must not follow current workout ownership.
-- +goose StatementBegin
DO $block$
DECLARE constraint_name text;
BEGIN
    SELECT conname INTO STRICT constraint_name FROM pg_constraint
     WHERE conrelid = 'app.workout_import_events'::regclass AND confrelid = 'app.workouts'::regclass
       AND contype = 'f';
    EXECUTE format('ALTER TABLE app.workout_import_events DROP CONSTRAINT %I', constraint_name);
END;
$block$;
-- +goose StatementEnd
ALTER TABLE app.workout_import_events ADD CONSTRAINT workout_import_events_workout_account_fk
    FOREIGN KEY(workout_id, account_id) REFERENCES app.workouts(id, account_id) ON DELETE RESTRICT;
ALTER TABLE app.workout_import_events DROP CONSTRAINT workout_import_events_kind_check;
ALTER TABLE app.workout_import_events ADD CONSTRAINT workout_import_events_kind_check
    CHECK (kind IN ('created', 'updated', 'matched_unchanged', 'adopted', 'skipped_live_owner'));

-- +goose StatementBegin
CREATE FUNCTION app.workout_provider_namespace(source_type text) RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT SET search_path = pg_catalog AS $function$
BEGIN
    IF source_type IN ('health-auto-export-local', 'health-auto-export-icloud') THEN
        RETURN 'health-auto-export';
    END IF;
    RAISE EXCEPTION 'unsupported workout provider namespace' USING ERRCODE = '22023';
END;
$function$;
-- +goose StatementEnd

CREATE TABLE app.workout_adoption_capabilities (
    backend_pid integer NOT NULL,
    transaction_id bigint NOT NULL,
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    old_source_id uuid NOT NULL,
    new_source_id uuid NOT NULL,
    source_file_id uuid NOT NULL,
    job_id uuid NOT NULL,
    PRIMARY KEY(backend_pid, transaction_id, workout_id),
    FOREIGN KEY(backend_pid, transaction_id)
        REFERENCES app.ingest_write_capabilities(backend_pid, transaction_id) ON DELETE CASCADE
);
ALTER TABLE app.workout_adoption_capabilities ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_adoption_capabilities FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_adoption_capabilities_owner_policy ON app.workout_adoption_capabilities
    TO workouts_security_owner USING (true) WITH CHECK (true);
REVOKE ALL ON app.workout_adoption_capabilities
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

-- No account UPDATE lock is held during matching. Epoch writes use NO KEY UPDATE
-- and NOWAIT: deletion/enqueue may already hold the account before waiting on sources.
-- +goose StatementBegin
CREATE FUNCTION app.bump_workout_ownership_epoch(target_account_id uuid) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE next_epoch bigint;
BEGIN
    PERFORM 1 FROM app.accounts account WHERE account.id = target_account_id
        AND account.id = app.current_account_id() FOR NO KEY UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid workout ownership account' USING ERRCODE = '42501'; END IF;
    UPDATE app.accounts account SET workout_ownership_epoch = account.workout_ownership_epoch + 1
     WHERE account.id = target_account_id RETURNING account.workout_ownership_epoch INTO next_epoch;
    RETURN next_epoch;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.source_workout_ownership_changed() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    PERFORM app.bump_workout_ownership_epoch(NEW.account_id);
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER sources_workout_ownership_after_delete
    AFTER UPDATE OF deleted_at ON app.sources FOR EACH ROW
    WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
    EXECUTE FUNCTION app.source_workout_ownership_changed();

-- A newly created source can be deleted after the fence's source snapshot. Its
-- epoch invalidates inventory, but is contention, never an authorization failure.
-- +goose StatementBegin
CREATE FUNCTION app.valid_ingest_ownership_epoch(target_account_id uuid, captured_epoch bigint, runtime_version integer) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF runtime_version IS NULL OR runtime_version < 26 OR NOT EXISTS (
        SELECT 1 FROM app.schema_metadata WHERE singleton AND minimum_runtime_version <= runtime_version
    ) THEN RETURN false; END IF;
    IF captured_epoch IS DISTINCT FROM (SELECT account.workout_ownership_epoch FROM app.accounts account
        WHERE account.id = target_account_id AND account.id = app.current_account_id()) THEN
        RAISE EXCEPTION 'workout ownership changed; retry transaction' USING ERRCODE = '55P03';
    END IF;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.fence_ingest_job(job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid) LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    RAISE EXCEPTION 'versioned ingest fence requires runtime 26 or newer' USING ERRCODE = '55000';
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.fence_ingest_job(job_id uuid, claiming_worker text, current_lease_token uuid, runtime_version integer)
RETURNS TABLE(source_id uuid) LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE target_account uuid; target_source uuid; target_parent uuid; child_kind text; captured_epoch bigint;
DECLARE existing_capability app.ingest_write_capabilities%ROWTYPE;
BEGIN
    IF runtime_version IS NULL OR runtime_version < 26 OR NOT EXISTS (
        SELECT 1 FROM app.schema_metadata WHERE singleton AND minimum_runtime_version <= runtime_version
    ) THEN RAISE EXCEPTION 'ingest runtime 26 or newer is required' USING ERRCODE = '55000'; END IF;
    IF claiming_worker IS NULL OR claiming_worker = '' OR current_lease_token IS NULL THEN
        RAISE EXCEPTION 'invalid ingest fence arguments' USING ERRCODE = '22023';
    END IF;
    SELECT capability.* INTO existing_capability FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current();
    IF FOUND THEN
        IF (existing_capability.account_id, existing_capability.job_id, existing_capability.worker_id,
            existing_capability.lease_token, existing_capability.runtime_version) IS DISTINCT FROM
           (app.current_account_id(), fence_ingest_job.job_id, claiming_worker, current_lease_token,
            fence_ingest_job.runtime_version) THEN
            RAISE EXCEPTION 'transaction already fenced for another ingest job' USING ERRCODE = '25001';
        END IF;
        -- The private capability proves this transaction already holds source,
        -- parent, and child locks. Clock expiry cannot transfer that authority.
        IF NOT app.valid_ingest_ownership_epoch(existing_capability.account_id,
            existing_capability.ownership_epoch, existing_capability.runtime_version) THEN
            RAISE EXCEPTION 'invalid ingest transaction fence' USING ERRCODE = '42501';
        END IF;
        PERFORM 1 FROM app.accounts account
          JOIN app.sources source ON source.account_id = account.id AND source.id = existing_capability.source_id
          JOIN app.job_config_snapshots snapshot ON snapshot.account_id = account.id AND snapshot.source_id = source.id
          JOIN app.jobs child ON child.id = snapshot.job_id AND child.account_id = account.id
          JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = account.id
         WHERE account.id = existing_capability.account_id AND account.state = 'active' AND source.deleted_at IS NULL
           AND child.id = existing_capability.job_id AND child.kind IN ('manual_ingest_source', 'scheduled_ingest_source')
           AND child.status = 'running' AND child.cancel_requested_at IS NULL
           AND child.worker_id = claiming_worker AND child.lease_token = current_lease_token
           AND parent.kind = CASE child.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END
           AND parent.status = 'running' AND parent.cancel_requested_at IS NULL;
        IF NOT FOUND THEN RAISE EXCEPTION 'invalid ingest transaction fence' USING ERRCODE = '42501'; END IF;
        source_id := existing_capability.source_id;
        RETURN NEXT;
        RETURN;
    END IF;
    SELECT job.account_id, snapshot.source_id, job.parent_job_id, job.kind
      INTO target_account, target_source, target_parent, child_kind
      FROM app.jobs job JOIN app.job_config_snapshots snapshot
        ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
     WHERE job.id = fence_ingest_job.job_id AND job.account_id = app.current_account_id()
       AND job.kind IN ('manual_ingest_source', 'scheduled_ingest_source');
    IF NOT FOUND OR target_parent IS NULL THEN RETURN; END IF;
    -- Serialize even the missing-identity case, before taking any source/job locks.
    IF NOT pg_try_advisory_xact_lock(hashtextextended('workout-ownership:' || target_account::text, 0)) THEN
        RAISE EXCEPTION 'workout ownership transaction is busy' USING ERRCODE = '55P03';
    END IF;
    -- Pre-acquire the account FK lock, not an account-wide UPDATE lock.
    PERFORM 1 FROM app.accounts account WHERE account.id = target_account AND account.state = 'active'
        FOR KEY SHARE NOWAIT;
    IF NOT FOUND THEN RETURN; END IF;
    PERFORM 1 FROM app.sources source WHERE source.account_id = target_account ORDER BY source.id FOR UPDATE NOWAIT;
    PERFORM 1 FROM app.sources source WHERE source.id = target_source AND source.account_id = target_account
        AND source.deleted_at IS NULL;
    IF NOT FOUND THEN RETURN; END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id = target_parent AND parent.account_id = target_account
        AND parent.kind = CASE child_kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END
        AND parent.status = 'running' AND parent.cancel_requested_at IS NULL FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id = fence_ingest_job.job_id AND job.account_id = target_account
        AND job.parent_job_id = target_parent AND job.kind = child_kind AND job.status = 'running'
        AND job.cancel_requested_at IS NULL AND job.worker_id = claiming_worker
        AND job.lease_token = current_lease_token AND job.lease_expires_at >= clock_timestamp() FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN; END IF;
    -- All type/header/route writes advance this row. Never wait on a Coverage writer
    -- while holding source/job locks; its workout FK lock is likewise NOWAIT below.
    PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'missing account data generation' USING ERRCODE = '55000'; END IF;
    SELECT account.workout_ownership_epoch INTO captured_epoch FROM app.accounts account WHERE account.id = target_account;
    IF NOT EXISTS (SELECT 1 FROM app.jobs job WHERE job.id = fence_ingest_job.job_id
        AND job.lease_expires_at >= clock_timestamp()) THEN RETURN; END IF;
    INSERT INTO app.ingest_write_capabilities
        (backend_pid, transaction_id, account_id, source_id, job_id, worker_id, lease_token, ownership_epoch, runtime_version)
    VALUES(pg_backend_pid(), txid_current(), target_account, target_source, fence_ingest_job.job_id,
        claiming_worker, current_lease_token, captured_epoch, fence_ingest_job.runtime_version)
    ON CONFLICT(backend_pid, transaction_id) DO NOTHING;
    IF NOT EXISTS (SELECT 1 FROM app.ingest_write_capabilities capability
        WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
          AND capability.account_id = target_account AND capability.source_id = target_source
          AND capability.job_id = fence_ingest_job.job_id AND capability.worker_id = claiming_worker
          AND capability.lease_token = current_lease_token AND capability.runtime_version = fence_ingest_job.runtime_version
          AND capability.ownership_epoch = captured_epoch) THEN
        RAISE EXCEPTION 'transaction already fenced for another ingest job' USING ERRCODE = '25001';
    END IF;
    source_id := target_source;
    RETURN NEXT;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.workout_deletion_suppressed(target_source_id uuid, target_provider_id text,
    target_fallback_fingerprint_version text, target_fallback_sha256 bytea) RETURNS boolean
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE namespace_value text;
BEGIN
    IF (target_provider_id IS NOT NULL)::integer + (target_fallback_sha256 IS NOT NULL)::integer <> 1 OR
       (target_fallback_sha256 IS NULL) <> (target_fallback_fingerprint_version IS NULL) OR
       (target_provider_id IS NOT NULL AND length(target_provider_id) NOT BETWEEN 1 AND 4096) OR
       (target_fallback_sha256 IS NOT NULL AND (octet_length(target_fallback_sha256) <> 32 OR
        length(target_fallback_fingerprint_version) NOT BETWEEN 1 AND 128)) THEN
        RAISE EXCEPTION 'invalid workout deletion identity' USING ERRCODE = '22023';
    END IF;
    SELECT app.workout_provider_namespace(source.type) INTO namespace_value FROM app.sources source
     WHERE source.id = target_source_id AND source.account_id = app.current_account_id();
    IF NOT FOUND THEN RETURN false; END IF;
    RETURN EXISTS (SELECT 1 FROM app.workout_deletion_targets target
        WHERE target.account_id = app.current_account_id() AND
          ((target_provider_id IS NOT NULL AND target.provider_namespace = namespace_value AND target.provider_id = target_provider_id) OR
           (target_provider_id IS NULL AND target.source_id = target_source_id AND target.provider_id IS NULL
            AND target.fallback_fingerprint_version = target_fallback_fingerprint_version AND target.fallback_sha256 = target_fallback_sha256)));
END;
$function$;
-- +goose StatementEnd

-- The worker writes the header and content in this same fenced file transaction.
-- No Coverage locks or scheduling belong in identity resolution.
-- +goose StatementBegin
CREATE FUNCTION app.resolve_ingest_workout_identity(job uuid, worker text, lease uuid, file uuid,
    proposed_workout_id uuid, provider_id text, fallback_version text, fallback_sha bytea)
RETURNS TABLE(workout_id uuid, prior_content_sha256 bytea, outcome text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE capability app.ingest_write_capabilities%ROWTYPE; namespace_value text; candidate app.workouts%ROWTYPE;
DECLARE candidate_count integer := 0; owner_deleted_at timestamptz; prior_source_id uuid;
BEGIN
    IF proposed_workout_id IS NULL THEN RAISE EXCEPTION 'invalid proposed workout identity' USING ERRCODE = '22023'; END IF;
    SELECT fence.* INTO capability FROM app.ingest_write_capabilities fence
      JOIN app.accounts account ON account.id = fence.account_id AND account.state = 'active'
      JOIN app.sources source ON source.id = fence.source_id AND source.account_id = fence.account_id AND source.deleted_at IS NULL
      JOIN app.job_config_snapshots snapshot ON snapshot.job_id = fence.job_id AND snapshot.account_id = fence.account_id
        AND snapshot.source_id = fence.source_id
      JOIN app.jobs child ON child.id = fence.job_id AND child.account_id = fence.account_id
      JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = fence.account_id
     WHERE fence.backend_pid = pg_backend_pid() AND fence.transaction_id = txid_current()
       AND fence.account_id = app.current_account_id() AND fence.job_id = job AND fence.worker_id = worker AND fence.lease_token = lease
       AND fence.runtime_version >= 26 AND fence.runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
       AND app.valid_ingest_ownership_epoch(fence.account_id, fence.ownership_epoch, fence.runtime_version) AND child.status = 'running'
        AND child.kind IN ('manual_ingest_source', 'scheduled_ingest_source')
        AND child.cancel_requested_at IS NULL AND child.worker_id = worker AND child.lease_token = lease
        AND parent.kind = CASE child.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END
        AND parent.status = 'running' AND parent.cancel_requested_at IS NULL;
    IF NOT FOUND THEN RAISE EXCEPTION 'workout identity requires a live transaction fence' USING ERRCODE = '42501'; END IF;
    PERFORM 1 FROM app.source_files source_file WHERE source_file.id = file AND source_file.account_id = capability.account_id
        AND source_file.source_id = capability.source_id AND source_file.job_id = job;
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid fenced workout source file' USING ERRCODE = '42501'; END IF;
    IF app.workout_deletion_suppressed(capability.source_id, provider_id, fallback_version, fallback_sha) THEN
        workout_id := NULL; prior_content_sha256 := NULL; outcome := 'suppressed'; RETURN NEXT; RETURN;
    END IF;
    SELECT app.workout_provider_namespace(source.type) INTO STRICT namespace_value FROM app.sources source
     WHERE source.id = capability.source_id AND source.account_id = capability.account_id AND source.deleted_at IS NULL;
    FOR candidate IN SELECT workout.* FROM app.workouts workout WHERE workout.account_id = capability.account_id
        AND ((resolve_ingest_workout_identity.provider_id IS NOT NULL AND workout.provider_namespace = namespace_value
              AND workout.provider_id = resolve_ingest_workout_identity.provider_id) OR
             (resolve_ingest_workout_identity.provider_id IS NULL AND workout.source_id = capability.source_id
              AND workout.provider_id IS NULL AND workout.fallback_fingerprint_version = fallback_version AND workout.fallback_sha256 = fallback_sha))
        ORDER BY workout.id FOR UPDATE NOWAIT
    LOOP
        candidate_count := candidate_count + 1;
        IF candidate_count > 1 THEN RAISE EXCEPTION 'workout identity-conflict' USING ERRCODE = '23514'; END IF;
        workout_id := candidate.id;
        prior_content_sha256 := candidate.content_sha256;
        prior_source_id := candidate.source_id;
        IF candidate.deletion_requested_at IS NOT NULL THEN
            outcome := 'suppressed';
        ELSIF candidate.source_id = capability.source_id THEN
            outcome := 'existing';
        ELSE
            SELECT source.deleted_at INTO STRICT owner_deleted_at FROM app.sources source
             WHERE source.id = candidate.source_id AND source.account_id = capability.account_id;
            outcome := CASE WHEN owner_deleted_at IS NULL THEN 'skipped_live_owner' ELSE 'adopted' END;
        END IF;
    END LOOP;
    IF candidate_count = 0 THEN
        IF EXISTS (SELECT 1 FROM app.workouts workout WHERE workout.id = proposed_workout_id) THEN
            RAISE EXCEPTION 'workout identity-conflict' USING ERRCODE = '23514';
        END IF;
        workout_id := proposed_workout_id; prior_content_sha256 := NULL; outcome := 'created';
    ELSIF outcome = 'adopted' THEN
        INSERT INTO app.workout_adoption_capabilities
            (backend_pid, transaction_id, account_id, workout_id, old_source_id, new_source_id, source_file_id, job_id)
        VALUES(pg_backend_pid(), txid_current(), capability.account_id, workout_id, prior_source_id, capability.source_id, file, job)
        ON CONFLICT ON CONSTRAINT workout_adoption_capabilities_pkey DO UPDATE SET source_file_id = EXCLUDED.source_file_id;
    END IF;
    RETURN NEXT;
END;
$function$;
-- +goose StatementEnd

-- Retain the deployed timezone/deletion exceptions verbatim; only strengthen the
-- ordinary ingest predicate. Source reassignment stays separate from that grant.
-- +goose StatementBegin
DO $block$
DECLARE definition text; anchor_count integer;
DECLARE anchor text := $pattern$(?i)\mAND[[:space:]]*\([[:space:]]*target_source_id[[:space:]]+IS[[:space:]]+NULL[[:space:]]+OR[[:space:]]+capability[[:space:]]*\.[[:space:]]*source_id[[:space:]]*=[[:space:]]*target_source_id[[:space:]]*\)$pattern$;
BEGIN
    definition := pg_get_functiondef('app.require_ingest_write_capability()'::regprocedure);
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN
        RAISE EXCEPTION 'unexpected ingest capability definition' USING ERRCODE = '55000';
    END IF;
    definition := regexp_replace(definition, anchor, 'AND capability.runtime_version >= 26
           AND capability.runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
           AND app.valid_ingest_ownership_epoch(capability.account_id, capability.ownership_epoch, capability.runtime_version)
           ' || E'\\&');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- Validate the captured epoch at commit, without promoting it after a decision.
-- +goose StatementBegin
DO $block$
DECLARE definition text; anchor_count integer;
DECLARE anchor text := $pattern$(?i)\mDELETE[[:space:]]+FROM[[:space:]]+app[[:space:]]*\.[[:space:]]*ingest_write_capabilities[[:space:]]+capability\M$pattern$;
BEGIN
    definition := pg_get_functiondef('app.clear_ingest_write_capability()'::regprocedure);
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'unexpected ingest cleanup definition' USING ERRCODE = '55000'; END IF;
    definition := regexp_replace(definition, anchor, 'IF NOT EXISTS (SELECT 1 FROM app.ingest_write_capabilities capability
        WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
          AND capability.account_id = NEW.account_id AND capability.job_id = NEW.job_id
          AND app.valid_ingest_ownership_epoch(capability.account_id, capability.ownership_epoch, capability.runtime_version)) THEN
        RAISE EXCEPTION ''ingest cleanup requires a versioned transaction fence'' USING ERRCODE = ''42501'';
    END IF;
    ' || E'\\&');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.enforce_ingest_identity() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF TG_TABLE_NAME = 'workouts' THEN
        IF NEW.provider_namespace IS DISTINCT FROM (SELECT app.workout_provider_namespace(source.type) FROM app.sources source
            WHERE source.id = NEW.source_id AND source.account_id = NEW.account_id) THEN
            RAISE EXCEPTION 'invalid workout provider namespace' USING ERRCODE = '23514';
        END IF;
        IF TG_OP = 'INSERT' AND app.workout_deletion_suppressed(NEW.source_id, NEW.provider_id,
            NEW.fallback_fingerprint_version, NEW.fallback_sha256) THEN
            RAISE EXCEPTION 'workout identity is suppressed' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'INSERT' THEN RETURN NEW; END IF;
    IF TG_TABLE_NAME = 'source_files' THEN
        IF (OLD.id, OLD.account_id, OLD.source_id, OLD.job_id, OLD.relative_name, OLD.size_bytes, OLD.modified_at, OLD.created_at)
            IS DISTINCT FROM (NEW.id, NEW.account_id, NEW.source_id, NEW.job_id, NEW.relative_name, NEW.size_bytes, NEW.modified_at, NEW.created_at) THEN
            RAISE EXCEPTION 'immutable source file identity changed' USING ERRCODE = '23514';
        END IF;
        IF OLD.state IN ('succeeded', 'failed') THEN RAISE EXCEPTION 'terminal source files are immutable' USING ERRCODE = '23514'; END IF;
        IF NEW.state IS DISTINCT FROM OLD.state AND NOT ((OLD.state = 'discovered' AND NEW.state IN ('processing', 'failed')) OR
            (OLD.state = 'processing' AND NEW.state IN ('succeeded', 'failed'))) THEN
            RAISE EXCEPTION 'invalid source file state transition' USING ERRCODE = '23514';
        END IF;
        IF OLD.checksum_sha256 IS NOT NULL AND NEW.checksum_sha256 IS DISTINCT FROM OLD.checksum_sha256 THEN
            RAISE EXCEPTION 'source file checksum is immutable once set' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'workout_types' THEN
        IF (OLD.id, OLD.account_id, OLD.type_key, OLD.created_at) IS DISTINCT FROM (NEW.id, NEW.account_id, NEW.type_key, NEW.created_at) THEN
            RAISE EXCEPTION 'immutable workout type identity changed' USING ERRCODE = '23514';
        END IF;
    ELSE
        IF (OLD.id, OLD.account_id, OLD.provider_namespace, OLD.provider_id, OLD.fallback_fingerprint_version, OLD.fallback_sha256, OLD.created_at)
            IS DISTINCT FROM (NEW.id, NEW.account_id, NEW.provider_namespace, NEW.provider_id, NEW.fallback_fingerprint_version, NEW.fallback_sha256, NEW.created_at) OR
            (OLD.provider_id IS NULL AND (OLD.workout_type_id, OLD.started_at) IS DISTINCT FROM (NEW.workout_type_id, NEW.started_at)) THEN
            RAISE EXCEPTION 'immutable workout provider identity changed' USING ERRCODE = '23514';
        END IF;
        IF OLD.source_id IS DISTINCT FROM NEW.source_id THEN
            IF OLD.provider_id IS NULL OR OLD.deletion_requested_at IS NOT NULL OR NEW.deletion_requested_at IS NOT NULL OR NOT EXISTS (
                SELECT 1 FROM app.workout_adoption_capabilities adoption
                  JOIN app.ingest_write_capabilities capability USING(backend_pid, transaction_id)
                  JOIN app.sources old_source ON old_source.id = adoption.old_source_id AND old_source.account_id = adoption.account_id
                 WHERE adoption.backend_pid = pg_backend_pid() AND adoption.transaction_id = txid_current()
                   AND adoption.account_id = OLD.account_id AND adoption.workout_id = OLD.id
                   AND adoption.old_source_id = OLD.source_id AND adoption.new_source_id = NEW.source_id
                   AND adoption.source_file_id = NEW.source_file_id AND adoption.job_id = capability.job_id
                   AND capability.source_id = NEW.source_id AND capability.account_id = NEW.account_id
                   AND capability.runtime_version >= 26 AND old_source.deleted_at IS NOT NULL
            ) THEN RAISE EXCEPTION 'workout adoption requires a resolved transaction capability' USING ERRCODE = '42501'; END IF;
            DELETE FROM app.workout_adoption_capabilities WHERE backend_pid = pg_backend_pid()
                AND transaction_id = txid_current() AND workout_id = NEW.id;
        END IF;
    END IF;
    NEW.updated_at := transaction_timestamp();
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER workouts_namespace_before_insert BEFORE INSERT ON app.workouts FOR EACH ROW EXECUTE FUNCTION app.enforce_ingest_identity();

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.workout_adoption_capabilities OWNER TO workouts_security_owner;
ALTER FUNCTION app.workout_provider_namespace(text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.bump_workout_ownership_epoch(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.source_workout_ownership_changed() OWNER TO workouts_security_owner;
ALTER FUNCTION app.valid_ingest_ownership_epoch(uuid,bigint,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.fence_ingest_job(uuid,text,uuid,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.resolve_ingest_workout_identity(uuid,text,uuid,uuid,uuid,text,text,bytea) OWNER TO workouts_security_owner;
ALTER FUNCTION app.enforce_ingest_identity() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT UPDATE(workout_ownership_epoch) ON app.accounts TO workouts_security_owner;
GRANT SELECT, UPDATE ON app.account_data_generations TO workouts_security_owner;
GRANT UPDATE(ownership_epoch, runtime_version) ON app.ingest_write_capabilities TO workouts_security_owner;
REVOKE ALL ON FUNCTION app.workout_provider_namespace(text), app.bump_workout_ownership_epoch(uuid),
    app.source_workout_ownership_changed(), app.valid_ingest_ownership_epoch(uuid,bigint,integer), app.enforce_ingest_identity(),
    app.fence_ingest_job(uuid,text,uuid,integer), app.resolve_ingest_workout_identity(uuid,text,uuid,uuid,uuid,text,text,bytea)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.fence_ingest_job(uuid,text,uuid,integer),
    app.resolve_ingest_workout_identity(uuid,text,uuid,uuid,uuid,text,text,bytea) TO workouts_worker;

-- Read retained provenance without exposing source configuration or credentials.
-- The definer sees tombstones; all tenant and deletion checks remain explicit.
-- +goose StatementBegin
CREATE FUNCTION app.read_workout_provenance(target_workout_id uuid)
RETURNS TABLE(event_id uuid, kind text, job_id uuid, source_id uuid, source_name text,
    source_type text, relative_name text, warnings jsonb, imported_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT event.id, event.kind, event.job_id, event.source_id,
        COALESCE(context.display_name, source.display_name), COALESCE(context.source_type, source.type),
        file.relative_name, event.warnings, event.created_at
      FROM app.workouts workout
      JOIN app.accounts account ON account.id = workout.account_id AND account.state = 'active'
      JOIN app.workout_import_events event ON event.workout_id = workout.id AND event.account_id = workout.account_id
      JOIN app.source_files file ON file.id = event.source_file_id AND file.account_id = event.account_id
        AND file.source_id = event.source_id AND file.job_id = event.job_id
      JOIN app.sources source ON source.id = event.source_id AND source.account_id = event.account_id
      LEFT JOIN app.job_source_contexts context ON context.job_id = event.job_id AND context.account_id = event.account_id
        AND context.source_id = event.source_id
     WHERE workout.id = target_workout_id AND workout.account_id = app.current_account_id()
       AND workout.deletion_requested_at IS NULL
     ORDER BY event.created_at, event.id
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.read_workout_provenance(uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT SELECT(id, account_id, workout_id, source_file_id, source_id, job_id, kind, warnings, created_at)
    ON app.workout_import_events TO workouts_security_owner;
REVOKE ALL ON FUNCTION app.read_workout_provenance(uuid)
    FROM PUBLIC, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.read_workout_provenance(uuid) TO workouts_api;

UPDATE app.schema_metadata SET schema_version = 24, minimum_runtime_version = 24 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'provider workout identity is forward-only' USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
