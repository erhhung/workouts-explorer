-- +goose Up
CREATE INDEX canonical_path_segments_start_graph_node_idx
ON osm_canonical.path_segments (start_graph_node_id);
CREATE INDEX canonical_path_segments_end_graph_node_idx
ON osm_canonical.path_segments (end_graph_node_id);

UPDATE osm_catalog.schema_metadata SET schema_version = 5 WHERE singleton;

-- +goose Down
DROP INDEX osm_canonical.canonical_path_segments_end_graph_node_idx;
DROP INDEX osm_canonical.canonical_path_segments_start_graph_node_idx;
UPDATE osm_catalog.schema_metadata SET schema_version = 4 WHERE singleton;
