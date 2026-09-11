-- +goose Up
ALTER TABLE app.coverage_diagnostic_runs DROP CONSTRAINT coverage_diagnostic_runs_path_policy_version_check;
ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT coverage_diagnostic_runs_path_policy_version_check
CHECK (path_policy_version IN ('coverage-path-policy-experimental-v1','coverage-path-policy-experimental-v2'));
UPDATE app.schema_metadata SET schema_version=16,minimum_runtime_version=15 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
DELETE FROM app.coverage_diagnostic_segment_labels label USING app.coverage_diagnostic_evidence evidence,app.coverage_diagnostic_runs run
 WHERE evidence.id=label.evidence_id AND run.id=evidence.run_id AND run.path_policy_version='coverage-path-policy-experimental-v2';
DELETE FROM app.coverage_diagnostic_overall_labels label USING app.coverage_diagnostic_runs run
 WHERE run.id=label.run_id AND run.path_policy_version='coverage-path-policy-experimental-v2';
DELETE FROM app.coverage_diagnostic_runs WHERE path_policy_version='coverage-path-policy-experimental-v2';
ALTER TABLE app.coverage_diagnostic_runs DROP CONSTRAINT coverage_diagnostic_runs_path_policy_version_check;
ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT coverage_diagnostic_runs_path_policy_version_check
CHECK (path_policy_version='coverage-path-policy-experimental-v1');
UPDATE app.schema_metadata SET schema_version=15,minimum_runtime_version=14 WHERE singleton;
