-- +goose Up
CREATE TABLE app.coverage_diagnostic_runs (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    route_input_revision bigint NOT NULL CHECK (route_input_revision>0),
    route_input_sha256 bytea NOT NULL CHECK (octet_length(route_input_sha256)=32),
    rules_version text NOT NULL CHECK (rules_version='coverage-experimental-v1'),
    sampling_version text NOT NULL CHECK (sampling_version='coverage-sampling-experimental-v1'),
    path_policy_version text NOT NULL CHECK (path_policy_version ~ '^coverage-path-policy-experimental-v[1-9][0-9]*$'),
    movement_mode text NOT NULL CHECK (movement_mode IN ('foot','bicycle','shared_public')),
    minimum_traversal_meters double precision NOT NULL CHECK (minimum_traversal_meters BETWEEN 0.1 AND 100),
    outcome text NOT NULL CHECK (outcome IN ('evaluated','no_evidence')),
    original_point_count integer NOT NULL CHECK (original_point_count BETWEEN 0 AND 25000),
    sampled_point_count integer NOT NULL CHECK (sampled_point_count BETWEEN 0 AND 10000),
    matched_point_count integer NOT NULL CHECK (matched_point_count>=0),
    ambiguous_point_count integer NOT NULL CHECK (ambiguous_point_count>=0),
    unmatched_point_count integer NOT NULL CHECK (unmatched_point_count>=0),
    rejected_point_count integer NOT NULL CHECK (rejected_point_count>=0),
    traversal_count integer NOT NULL CHECK (traversal_count>=0),
    portion_count integer NOT NULL CHECK (portion_count BETWEEN 0 AND 10000),
    unique_segment_count integer NOT NULL CHECK (unique_segment_count BETWEEN 0 AND 4096),
    duration_milliseconds integer NOT NULL CHECK (duration_milliseconds>=0),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id,id),
    UNIQUE (id),
    FOREIGN KEY (workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);

CREATE TABLE app.coverage_diagnostic_generations (
    account_id uuid NOT NULL,
    run_id uuid NOT NULL,
    region_id text NOT NULL CHECK (region_id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'),
    generation_id bigint NOT NULL CHECK (generation_id>0),
    source_url text NOT NULL CHECK (length(source_url) BETWEEN 1 AND 4096),
    source_sha256 bytea NOT NULL CHECK (octet_length(source_sha256)=32),
    source_header_timestamp timestamptz,
    importer_version integer NOT NULL CHECK (importer_version>0),
    derivation_version integer NOT NULL CHECK (derivation_version>0),
    promoted_at timestamptz NOT NULL,
    PRIMARY KEY (account_id,run_id,region_id,generation_id),
    FOREIGN KEY (account_id,run_id) REFERENCES app.coverage_diagnostic_runs(account_id,id) ON DELETE CASCADE
);

CREATE TABLE app.coverage_diagnostic_evidence (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    run_id uuid NOT NULL,
    portion_ordinal integer NOT NULL CHECK (portion_ordinal BETWEEN 0 AND 9999),
    physical_segment_id uuid NOT NULL,
    direction text NOT NULL CHECK (direction IN ('forward','reverse')),
    region_id text NOT NULL,
    generation_id bigint NOT NULL,
    derivation_version integer NOT NULL CHECK (derivation_version>0),
    logical_path_id uuid NOT NULL,
    locality_relation_id bigint,
    source_way_id bigint NOT NULL,
    source_way_version integer NOT NULL CHECK (source_way_version>0),
    source_from_fraction double precision NOT NULL CHECK (source_from_fraction>=0 AND source_from_fraction<source_to_fraction),
    source_to_fraction double precision NOT NULL CHECK (source_to_fraction<=1),
    traversed_meters double precision NOT NULL CHECK (traversed_meters>0),
    evidence_class text NOT NULL CHECK (evidence_class IN ('matched','ambiguous')),
    geom geometry(LineString,4326) NOT NULL CHECK (NOT ST_IsEmpty(geom) AND ST_IsValid(geom)),
    PRIMARY KEY (account_id,run_id,id),
    UNIQUE (account_id,run_id,portion_ordinal),
    FOREIGN KEY (account_id,run_id) REFERENCES app.coverage_diagnostic_runs(account_id,id) ON DELETE CASCADE,
    FOREIGN KEY (account_id,run_id,region_id,generation_id) REFERENCES app.coverage_diagnostic_generations(account_id,run_id,region_id,generation_id)
);

CREATE TABLE app.coverage_diagnostic_overall_labels (
    account_id uuid NOT NULL,
    run_id uuid NOT NULL,
    label text NOT NULL CHECK (label IN ('correct','incorrect','uncertain')),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id,run_id),
    FOREIGN KEY (account_id,run_id) REFERENCES app.coverage_diagnostic_runs(account_id,id) ON DELETE CASCADE
);

CREATE TABLE app.coverage_diagnostic_segment_labels (
    account_id uuid NOT NULL,
    run_id uuid NOT NULL,
    evidence_id uuid NOT NULL,
    label text NOT NULL CHECK (label IN ('expected','unexpected','uncertain')),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id,run_id,evidence_id),
    FOREIGN KEY (account_id,run_id,evidence_id) REFERENCES app.coverage_diagnostic_evidence(account_id,run_id,id) ON DELETE CASCADE
);

ALTER TABLE app.coverage_diagnostic_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_generations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_generations FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_evidence FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_overall_labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_overall_labels FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_segment_labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_diagnostic_segment_labels FORCE ROW LEVEL SECURITY;

CREATE POLICY coverage_diagnostic_runs_account_policy ON app.coverage_diagnostic_runs USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY coverage_diagnostic_generations_account_policy ON app.coverage_diagnostic_generations USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY coverage_diagnostic_evidence_account_policy ON app.coverage_diagnostic_evidence USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY coverage_diagnostic_overall_labels_account_policy ON app.coverage_diagnostic_overall_labels USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY coverage_diagnostic_segment_labels_account_policy ON app.coverage_diagnostic_segment_labels USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY coverage_diagnostic_runs_owner_policy ON app.coverage_diagnostic_runs TO workouts_security_owner USING (true) WITH CHECK (true);
CREATE POLICY coverage_diagnostic_generations_owner_policy ON app.coverage_diagnostic_generations TO workouts_security_owner USING (true) WITH CHECK (true);
CREATE POLICY coverage_diagnostic_evidence_owner_policy ON app.coverage_diagnostic_evidence TO workouts_security_owner USING (true) WITH CHECK (true);
CREATE POLICY coverage_diagnostic_overall_labels_owner_policy ON app.coverage_diagnostic_overall_labels TO workouts_security_owner USING (true) WITH CHECK (true);
CREATE POLICY coverage_diagnostic_segment_labels_owner_policy ON app.coverage_diagnostic_segment_labels TO workouts_security_owner USING (true) WITH CHECK (true);

-- +goose StatementBegin
CREATE FUNCTION app.persist_coverage_diagnostic(
    target_account_id uuid,target_run_id uuid,target_workout_id uuid,target_revision bigint,target_digest bytea,
    target_rules text,target_sampling text,target_policy text,target_mode text,target_minimum double precision,
    target_outcome text,target_original integer,target_sampled integer,target_matched integer,target_ambiguous integer,
    target_unmatched integer,target_rejected integer,target_traversals integer,target_portions integer,
    target_unique_segments integer,target_duration integer,target_generations jsonb,target_evidence jsonb
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app,public AS $$
DECLARE item jsonb; evidence_count integer:=0;
BEGIN
    IF target_account_id<>app.current_account_id() OR jsonb_typeof(target_generations)<>'array' OR jsonb_typeof(target_evidence)<>'array'
       OR jsonb_array_length(target_evidence)>10000 THEN
        RAISE EXCEPTION 'invalid coverage diagnostic' USING ERRCODE='22023';
    END IF;
    PERFORM 1 FROM app.workout_coverage_states state
      JOIN app.workouts workout ON workout.id=state.workout_id AND workout.account_id=state.account_id
     WHERE state.account_id=target_account_id AND state.workout_id=target_workout_id
       AND workout.deletion_requested_at IS NULL AND state.route_input_revision=target_revision
       AND state.route_input_sha256=target_digest FOR UPDATE OF state;
    IF NOT FOUND THEN RETURN false; END IF;
    INSERT INTO app.coverage_diagnostic_runs VALUES(target_run_id,target_account_id,target_workout_id,target_revision,target_digest,
        target_rules,target_sampling,target_policy,target_mode,target_minimum,target_outcome,target_original,target_sampled,
        target_matched,target_ambiguous,target_unmatched,target_rejected,target_traversals,target_portions,target_unique_segments,target_duration,transaction_timestamp());
    FOR item IN SELECT value FROM jsonb_array_elements(target_generations) LOOP
        INSERT INTO app.coverage_diagnostic_generations(account_id,run_id,region_id,generation_id,source_url,source_sha256,
            source_header_timestamp,importer_version,derivation_version,promoted_at)
        VALUES(target_account_id,target_run_id,item->>'regionId',(item->>'generation')::bigint,item->>'sourceUrl',
            decode(item->>'sourceSha256','base64'),NULLIF(item->>'sourceHeaderTimestamp','')::timestamptz,
            (item->>'importerVersion')::integer,(item->>'derivationVersion')::integer,(item->>'promotedAt')::timestamptz);
    END LOOP;
    FOR item IN SELECT value FROM jsonb_array_elements(target_evidence) LOOP
        evidence_count:=evidence_count+1;
        INSERT INTO app.coverage_diagnostic_evidence(id,account_id,run_id,portion_ordinal,physical_segment_id,direction,
            region_id,generation_id,derivation_version,logical_path_id,locality_relation_id,source_way_id,source_way_version,
            source_from_fraction,source_to_fraction,traversed_meters,evidence_class,geom)
        VALUES((item->>'id')::uuid,target_account_id,target_run_id,(item->>'ordinal')::integer,(item->>'segmentId')::uuid,
            item->>'direction',item->>'regionId',(item->>'generation')::bigint,(item->>'derivationVersion')::integer,
            (item->>'logicalPathId')::uuid,NULLIF(item->>'localityRelationId','')::bigint,(item->>'sourceWayId')::bigint,
            (item->>'sourceWayVersion')::integer,(item->>'sourceFromFraction')::double precision,
            (item->>'sourceToFraction')::double precision,(item->>'traversedMeters')::double precision,item->>'evidenceClass',
            ST_SetSRID(ST_GeomFromGeoJSON(item->'geometry'),4326));
    END LOOP;
    IF evidence_count<>target_portions THEN RAISE EXCEPTION 'invalid coverage evidence count' USING ERRCODE='22023'; END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.set_coverage_diagnostic_labels(target_account_id uuid,target_run_id uuid,set_overall boolean,new_overall text,new_segments jsonb) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$
DECLARE item jsonb;
BEGIN
    IF target_account_id<>app.current_account_id() OR jsonb_typeof(new_segments)<>'array' OR jsonb_array_length(new_segments)>4096
       OR (set_overall AND new_overall NOT IN ('correct','incorrect','uncertain')) THEN
        RAISE EXCEPTION 'invalid coverage diagnostic labels' USING ERRCODE='22023';
    END IF;
    PERFORM 1 FROM app.coverage_diagnostic_runs WHERE account_id=target_account_id AND id=target_run_id;
    IF NOT FOUND THEN RAISE no_data_found; END IF;
    IF set_overall THEN
        INSERT INTO app.coverage_diagnostic_overall_labels(account_id,run_id,label) VALUES(target_account_id,target_run_id,new_overall)
        ON CONFLICT (account_id,run_id) DO UPDATE SET label=EXCLUDED.label,updated_at=transaction_timestamp();
    END IF;
    DELETE FROM app.coverage_diagnostic_segment_labels WHERE account_id=target_account_id AND run_id=target_run_id;
    FOR item IN SELECT value FROM jsonb_array_elements(new_segments) LOOP
        INSERT INTO app.coverage_diagnostic_segment_labels(account_id,run_id,evidence_id,label)
        VALUES(target_account_id,target_run_id,(item->>'evidenceId')::uuid,item->>'label');
    END LOOP;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON app.coverage_diagnostic_runs,app.coverage_diagnostic_generations,app.coverage_diagnostic_evidence,
    app.coverage_diagnostic_overall_labels,app.coverage_diagnostic_segment_labels FROM PUBLIC,workouts_api,workouts_worker;
GRANT SELECT ON app.coverage_diagnostic_runs,app.coverage_diagnostic_generations,app.coverage_diagnostic_evidence,
    app.coverage_diagnostic_overall_labels,app.coverage_diagnostic_segment_labels TO workouts_api;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.coverage_diagnostic_runs OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_diagnostic_generations OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_diagnostic_evidence OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_diagnostic_overall_labels OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_diagnostic_segment_labels OWNER TO workouts_security_owner;
ALTER FUNCTION app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb),
    app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb) FROM PUBLIC,workouts_api,workouts_worker;
GRANT EXECUTE ON FUNCTION app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb),
    app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb) TO workouts_api;

UPDATE app.schema_metadata SET schema_version=14,minimum_runtime_version=13 WHERE singleton;

-- +goose Down
UPDATE app.schema_metadata SET schema_version=13,minimum_runtime_version=12 WHERE singleton;
DROP FUNCTION app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb);
DROP FUNCTION app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb);
DROP TABLE app.coverage_diagnostic_segment_labels;
DROP TABLE app.coverage_diagnostic_overall_labels;
DROP TABLE app.coverage_diagnostic_evidence;
DROP TABLE app.coverage_diagnostic_generations;
DROP TABLE app.coverage_diagnostic_runs;
