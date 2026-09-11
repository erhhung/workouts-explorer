package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestCanonicalRegionMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("00003_canonical_regions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	required := []string{
		"CREATE TABLE osm_canonical.ways",
		"CREATE TABLE osm_canonical.localities",
		"CREATE TABLE osm_canonical.path_segments",
		"FOREIGN KEY (region_id, generation_id)",
		"(preferred.version,preferred.generation_id,preferred.region_id)",
		"> (contribution.version,contribution.generation_id,contribution.region_id)",
		"(preferred.relation_version,preferred.generation_id,preferred.region_id)",
		"> (contribution.relation_version,contribution.generation_id,contribution.region_id)",
		"> (segment.source_way_version,segment.generation_id,segment.region_id)",
		"preferred_segment.segment_id = segment.segment_id",
		"FROM osm_active.path_segments",
		"pg_advisory_xact_lock(hashtextextended(promote_region_id, 0))",
		"state IN ('building', 'validating')",
		"Target-generation canonical rows are staging rows until catalog activation",
		"generation.state = 'active'",
		"generation % has path segments without matching source way versions",
		"staged_logical_path_count <> build_logical_path_count",
		"generation_id <> promote_generation_id",
		"SET state = 'retired'",
		"SET state = 'active', validation = promote_validation",
		"WHERE state = 'active'",
		"RAISE EXCEPTION 'cannot downgrade canonical OSM schema with % active regions'",
		"CREATE VIEW osm_active.ways AS SELECT * FROM %I.ways",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration is missing contract fragment %q", fragment)
		}
	}
}

func TestPromotionScriptDelegatesToCanonicalFunction(t *testing.T) {
	contents, err := os.ReadFile("../../../osm/promote.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	if !strings.Contains(sql, "osm_catalog.promote_region_generation(") {
		t.Fatal("promotion script does not call canonical promotion function")
	}
	for _, forbidden := range []string{"UPDATE osm_catalog.generations", "CREATE OR REPLACE VIEW"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("promotion script contains orchestration logic %q", forbidden)
		}
	}
}

func TestStorageBoundedPartitionMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("00004_storage_bounded_partitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, fragment := range []string{
		"PARTITION BY LIST (region_id)",
		"exactly one active region for nonempty schema-3 canonical heaps",
		"ATTACH PARTITION",
		"DETACH PARTITION",
		"CREATE TABLE osm_catalog.region_storage",
		"CREATE TABLE osm_catalog.storage_gc",
		"validation does not match its source versions and preparation",
		"cannot downgrade schema 4 after the first partition replacement",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("schema-4 migration is missing contract fragment %q", fragment)
		}
	}
	for _, forbidden := range []string{"INSERT INTO osm_canonical.ways\n         SELECT", "DELETE FROM osm_canonical.ways"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("schema-4 migration contains copying promotion fragment %q", forbidden)
		}
	}
	for _, fragment := range []string{"FULL JOIN %1$I.logical_paths stored", "IS DISTINCT FROM", "abs(derived.member_length_m-stored.member_length_m) > 0.01"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("schema-4 promotion source is missing tolerant logical-path check %q", fragment)
		}
	}
	if strings.Contains(sql, "EXCEPT SELECT * FROM %1$I.logical_paths") {
		t.Error("schema-4 promotion source still contains exact logical-path EXCEPT comparison")
	}
}

func TestPartitionPreparationContract(t *testing.T) {
	contents, err := os.ReadFile("../../../osm/prepare-partitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, fragment := range []string{"ADD COLUMN region_id", "ADD COLUMN generation_id", "VALIDATE CONSTRAINT", "ALTER COLUMN version SET NOT NULL", "ALTER COLUMN relation_version SET NOT NULL", "ALTER COLUMN geom SET NOT NULL", "PRIMARY KEY(region_id,generation_id", "ways_g", "path_segments_g", "path_segments_start_graph_node_g", "path_segments_end_graph_node_g"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("partition preparation is missing contract fragment %q", fragment)
		}
	}
}

func TestPartitionValidationContract(t *testing.T) {
	contents, err := os.ReadFile("../../../osm/validate.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, fragment := range []string{"partitionPrepared", "preparedRegionId", "preparedGenerationId", "provenanceMismatches", "sourceVersionMismatches", "logicalPathMismatches", "missingEndpointIndexes", "importerVersion", "derivationVersion"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("partition validation is missing gate %q", fragment)
		}
	}
	for _, fragment := range []string{"FULL JOIN", "IS DISTINCT FROM", "abs(derived.member_length_m-stored.member_length_m) > 0.01"} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("partition validation is missing tolerant logical-path check %q", fragment)
		}
	}
	if strings.Contains(sql, "EXCEPT") {
		t.Error("partition validation still contains an exact EXCEPT comparison")
	}
}

func TestTolerantLogicalPathPromotionMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("00006_tolerant_logical_path_promotion.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	parts := strings.Split(sql, "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("schema-6 migration must have exactly one Goose Down section")
	}
	up, down := parts[0], parts[1]
	for _, fragment := range []string{
		"-- +goose StatementBegin",
		"CREATE OR REPLACE FUNCTION osm_catalog.promote_region_generation(",
		"pg_advisory_xact_lock(hashtextextended(promote_region_id, 0))",
		"DETACH PARTITION",
		"ATTACH PARTITION",
		"INSERT INTO osm_catalog.storage_gc",
		"INSERT INTO osm_catalog.region_storage",
		"FULL JOIN %1$I.logical_paths stored",
		"IS DISTINCT FROM",
		"abs(derived.member_length_m-stored.member_length_m) > 0.01",
		"UPDATE osm_catalog.schema_metadata SET schema_version = 6",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("schema-6 migration Up is missing %q", fragment)
		}
	}
	if strings.Contains(up, "EXCEPT SELECT") {
		t.Error("effective schema-6 promotion still contains exact logical-path EXCEPT comparison")
	}
	for _, forbidden := range []string{"pg_get_functiondef", "regexp_replace", "replace(definition", "EXECUTE replace"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("schema-6 migration dynamically rewrites a function definition with %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION osm_catalog.promote_region_generation(",
		"EXCEPT SELECT * FROM %1$I.logical_paths",
		"SELECT * FROM %1$I.logical_paths EXCEPT",
		"INSERT INTO osm_catalog.storage_gc",
		"INSERT INTO osm_catalog.region_storage",
		"UPDATE osm_catalog.schema_metadata SET schema_version = 5",
	} {
		if !strings.Contains(down, fragment) {
			t.Errorf("schema-6 migration Down does not restore schema 5 fragment %q", fragment)
		}
	}
}

func TestOutsideLogicalPathsAreProviderRegionScoped(t *testing.T) {
	for _, path := range []string{"../../../osm/derive-compact.sql", "../../../osm/clip-localities.sql"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(contents)
		for _, required := range []string{"workouts_explorer.osm_region_id", "osm-logical-path/v2:region:"} {
			if !strings.Contains(sql, required) {
				t.Errorf("%s is missing %q", path, required)
			}
		}
		if strings.Contains(sql, "osm-logical-path/v1:outside:") {
			t.Errorf("%s still contains globally merged outside logical paths", path)
		}
	}
}

func TestMatcherEndpointIndexMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("00005_matcher_endpoint_indexes.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, fragment := range []string{
		"canonical_path_segments_start_graph_node_idx\nON osm_canonical.path_segments (start_graph_node_id)",
		"canonical_path_segments_end_graph_node_idx\nON osm_canonical.path_segments (end_graph_node_id)",
		"UPDATE osm_catalog.schema_metadata SET schema_version = 5",
		"UPDATE osm_catalog.schema_metadata SET schema_version = 4",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("schema-5 migration is missing contract fragment %q", fragment)
		}
	}
	if strings.Contains(sql, "USING gist") || strings.Contains(sql, "USING gin") {
		t.Error("schema-5 endpoint indexes are not B-tree indexes")
	}
}

func TestCanonicalOverlapFixtureDocumentsReplacementInvariants(t *testing.T) {
	contents, err := os.ReadFile("testdata/canonical_overlap.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, invariant := range []string{
		"promote_region_generation('fixture:region-a'",
		"promote_region_generation('fixture:region-b'",
		"newer source way version did not win",
		"segment from superseded source way is visible",
		"exact segment identity was not deduplicated",
		"replacing one region removed the other region",
		"retired region leaves were not queued for GC",
		"forced rollback changed active storage",
		"ROLLBACK",
	} {
		if !strings.Contains(sql, invariant) {
			t.Errorf("overlap fixture is missing invariant %q", invariant)
		}
	}
}
