package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestCanonicalRegionMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("003_canonical_regions.sql")
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
	contents, err := Files.ReadFile("004_storage_bounded_partitions.sql")
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
	contents, err := Files.ReadFile("006_tolerant_logical_path_promotion.sql")
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

func TestPromotionEventsMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("007_promotion_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"CREATE TABLE osm_catalog.promotion_events", "UNIQUE (region_id,generation_id)",
		"CREATE TRIGGER generations_record_promotion_event", "NEW.state='active'",
		"CREATE FUNCTION osm_catalog.read_promotion_events", "ORDER BY event.id",
		"CREATE FUNCTION osm_catalog.promotion_event_head",
		"schema_version=7,minimum_runtime_version=6",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("promotion event migration missing %q", required)
		}
	}
}

func TestConnectedAttributionPromotionMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("008_connected_attribution_promotion.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"RENAME TO promote_region_generation_v7",
		"'remainingConnectedAttributionSplits',0",
		"PERFORM osm_catalog.promote_region_generation_v7",
		"schema_version=8,minimum_runtime_version=8",
	} {
		if !strings.Contains(string(contents), required) {
			t.Errorf("connected attribution promotion migration missing %q", required)
		}
	}
}

func TestParkAttributionDerivationContract(t *testing.T) {
	for name, fragments := range map[string][]string{
		"../../../osm/import.lua": {"park_ways", "park_relations", "object.is_closed", "object:as_polygon()",
			"tags.boundary == 'national_park'", "tags.protected_area == 'national_park'", "string.lower(protection_title)",
			"education_ways", "education_relations", "tags.amenity == 'school'", "tags.amenity == 'university'", "tags.landuse == 'education'"},
		"../../../osm/postprocess.sql": {"park_areas", "'national_park'", "area_m2 BETWEEN 500 AND 25000000",
			"area_m2 BETWEEN 1000000 AND 100000000000", "park_kind='national_park' OR tags->>'protect_class' IS DISTINCT FROM '2'",
			"WHEN 'protected_area' THEN 3 WHEN 'national_park' THEN 4", "admin_level' IN ('6','8')",
			"education_areas", "education_kind='school'", "education_kind='university'", "area_m2 BETWEEN 5000 AND 500000000"},
		"../../../osm/attribute-parks.sql": {"ST_Difference(segment.geom,candidate.geom)", "workouts:park_id",
			"locality.admin_level=8", "county.admin_level=6", "middle.length_m<=25",
			"workouts:park_kind", "workouts-explorer/osm-park/v1", "workouts-explorer/osm-national-park/v1", ":'OSM_REGION_ID'",
			"workouts:education_id", "workouts:education_name", "workouts-explorer/osm-education/v1:",
			"ORDER BY candidate.type_priority,candidate.area_m2", "attribution_identity_edges",
			"workouts-explorer/osm-attribution-group/v2:", "attribution_identity_branch_nodes", "attribution_identity_branch_classes", "AND edge.unnamed", "left_source_way_id<>edge.right_source_way_id",
			"workouts-explorer/osm-named-road-proximity/v1:", "ST_DWithin(a.geom::geography,candidate.geom::geography,50)", "a.oneway AND candidate.oneway",
			"parent_segment_id", "workouts-explorer/osm-logical-path/v12:group:", "remaining_connected_splits",
			"TRUNCATE logical_paths", "GROUP BY logical_path_id"},
		"../../../osm/validate.sql": {"qualifyingParkAreas", "qualifyingNationalParkAreas", "invalidNationalParkAreas",
			"parkAttributedSegments", "nationalParkAttributedSegments", "attributionIdentityLogicalIdEdges",
			"attributionIdentityAffectedLogicalIds", "attributionIdentityMergedComponents", "attributionScopeRebasedSegments",
			"attributionScopeRebasedLogicalIds", "remainingConnectedAttributionSplits",
			"invalidParkAttributions", "materialParkResiduals", "qualifyingEducationAreas",
			"educationAttributedSegments", "invalidEducationAttributions", "materialEducationResiduals"},
	} {
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(contents), fragment) {
				t.Errorf("%s missing %q", name, fragment)
			}
		}
	}
}

func TestConnectedAttributionIdentityFixtureContract(t *testing.T) {
	contents, err := os.ReadFile("../../../osm/testdata/connected-attribution-identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"E5A7797B8C532C72E23085275E2EBA82", "CAEF72F19BB476189D79274DC9B267EE",
		"'named-road'", "'named-footway'", "'other-city'", "'park-contained'",
		"'different-name'", "'disconnected'", "broad-class boundary",
	} {
		if !strings.Contains(string(contents), fragment) {
			t.Errorf("connected attribution fixture missing %q", fragment)
		}
	}
}

func TestConnectedAttributionIdentityUsesIndexedTopologyAndClass(t *testing.T) {
	contents, err := os.ReadFile("../../../osm/attribute-parks.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	start := strings.Index(sql, "CREATE UNLOGGED TABLE attribution_identity_edges")
	end := strings.Index(sql, "\\echo 'OSM progress: rewriting segments with merged logical path identities'")
	if start < 0 || end <= start {
		t.Fatal("connected attribution component derivation block is missing")
	}
	derivation := sql[start:end]
	if !strings.Contains(sql, "WHEN normalized_name IS NOT NULL THEN 'named'") ||
		!strings.Contains(sql, "WHEN broad_class='road' THEN 'road' ELSE 'path' END AS attribution_class") {
		t.Error("connected attribution group does not preserve named bridging and unnamed road/path boundaries")
	}
	for _, required := range []string{
		"group_id uuid NOT NULL",
		"b.start_graph_node_id=a.start_graph_node_id",
		"b.end_graph_node_id=a.start_graph_node_id",
		"b.start_graph_node_id=a.end_graph_node_id",
		"b.end_graph_node_id=a.end_graph_node_id",
		"SET enable_hashjoin = off", "SET enable_mergejoin = off",
	} {
		if !strings.Contains(derivation, required) {
			t.Errorf("connected attribution derivation missing bounded join contract %q", required)
		}
	}
	componentStart := strings.Index(derivation, "CREATE UNLOGGED TABLE attribution_identity_components")
	if componentStart < 0 {
		t.Fatal("connected attribution component table is missing")
	}
	edgeDefinition := derivation[:componentStart]
	if strings.Contains(edgeDefinition, "scope_kind text") || strings.Contains(edgeDefinition, "name_key text") {
		t.Error("cross-logical-ID edges repeat scope/name text instead of using the compact group UUID")
	}
}

func TestConnectedAttributionValidationUsesRecordedRequiredEdges(t *testing.T) {
	contents, err := os.ReadFile("../../../osm/validate.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	if !strings.Contains(sql, "SELECT remaining_connected_splits FROM :\"OSM_BUILD_SCHEMA\".attribution_identity_stats") {
		t.Error("validation does not use the exact required-edge split count recorded during derivation")
	}
	if strings.Contains(sql, "b.start_graph_node_id=a.start_graph_node_id") {
		t.Error("validation repeats the expensive endpoint graph derivation")
	}
}

func TestNationalParkSelectiveFilterContract(t *testing.T) {
	contents, err := os.ReadFile("../../../internal/osm/update_pipeline.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"w/protected_area=national_park", "w/protection_title",
		"r/protected_area=national_park", "r/protection_title", "w/amenity=school,college,university",
		"r/amenity=school,college,university", "w/landuse=education", "r/landuse=education"} {
		if !strings.Contains(string(contents), fragment) {
			t.Errorf("selective filter is missing %q", fragment)
		}
	}
}

func TestMatcherEndpointIndexMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("005_matcher_endpoint_indexes.sql")
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
