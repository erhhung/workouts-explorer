-- +goose Up
ALTER TABLE app.coverage_diagnostic_runs DROP CONSTRAINT coverage_diagnostic_runs_path_policy_version_check;
ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT coverage_diagnostic_runs_path_policy_version_check
CHECK (path_policy_version ~ '^coverage-path-policy-experimental-v[1-9][0-9]*$');
UPDATE app.schema_metadata SET schema_version=20,minimum_runtime_version=19 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
DELETE FROM app.coverage_diagnostic_segment_labels label USING app.coverage_diagnostic_evidence evidence,app.coverage_diagnostic_runs run
 WHERE evidence.id=label.evidence_id AND run.id=evidence.run_id
   AND run.path_policy_version NOT IN ('coverage-path-policy-experimental-v1','coverage-path-policy-experimental-v2','coverage-path-policy-experimental-v3','coverage-path-policy-experimental-v4','coverage-path-policy-experimental-v5');
DELETE FROM app.coverage_diagnostic_overall_labels label USING app.coverage_diagnostic_runs run
 WHERE run.id=label.run_id
   AND run.path_policy_version NOT IN ('coverage-path-policy-experimental-v1','coverage-path-policy-experimental-v2','coverage-path-policy-experimental-v3','coverage-path-policy-experimental-v4','coverage-path-policy-experimental-v5');
DELETE FROM app.coverage_diagnostic_runs
 WHERE path_policy_version NOT IN ('coverage-path-policy-experimental-v1','coverage-path-policy-experimental-v2','coverage-path-policy-experimental-v3','coverage-path-policy-experimental-v4','coverage-path-policy-experimental-v5');
ALTER TABLE app.coverage_diagnostic_runs DROP CONSTRAINT coverage_diagnostic_runs_path_policy_version_check;
ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT coverage_diagnostic_runs_path_policy_version_check
CHECK (path_policy_version IN ('coverage-path-policy-experimental-v1','coverage-path-policy-experimental-v2','coverage-path-policy-experimental-v3','coverage-path-policy-experimental-v4','coverage-path-policy-experimental-v5'));
UPDATE app.schema_metadata SET schema_version=19,minimum_runtime_version=18 WHERE singleton;
