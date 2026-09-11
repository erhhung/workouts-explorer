-- +goose Up
UPDATE app.schema_metadata SET schema_version=22,minimum_runtime_version=21 WHERE singleton;

-- +goose Down
UPDATE app.schema_metadata SET schema_version=21,minimum_runtime_version=20 WHERE singleton;
