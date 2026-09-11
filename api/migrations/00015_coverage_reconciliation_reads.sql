-- +goose Up
GRANT SELECT(account_id,workout_id) ON app.workout_routes TO workouts_worker;
GRANT SELECT(account_id,workout_id,readiness_state) ON app.workout_coverage_states TO workouts_worker;
GRANT SELECT(account_id,workout_id,region_id,desired_osm_generation) ON app.workout_coverage_regions TO workouts_worker;

UPDATE app.schema_metadata SET schema_version=15,minimum_runtime_version=14 WHERE singleton;

-- +goose Down
UPDATE app.schema_metadata SET schema_version=14,minimum_runtime_version=13 WHERE singleton;
REVOKE SELECT(account_id,workout_id,region_id,desired_osm_generation) ON app.workout_coverage_regions FROM workouts_worker;
REVOKE SELECT(account_id,workout_id,readiness_state) ON app.workout_coverage_states FROM workouts_worker;
REVOKE SELECT(account_id,workout_id) ON app.workout_routes FROM workouts_worker;
