package migrations

import (
	"os"
	"strings"
	"testing"
)

func containsSQL(sql, fragment string) bool {
	return strings.Contains(strings.Join(strings.Fields(sql), ""), strings.Join(strings.Fields(fragment), ""))
}

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
		if !containsSQL(sql, fragment) {
			t.Errorf("migration is missing contract fragment %q", fragment)
		}
	}
}

func TestResumableStageAndStorageBlockContract(t *testing.T) {
	stageContents, err := Files.ReadFile("008_resumable_generation_stages.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"cursor jsonb", "batch_count bigint", "expected_batch_count", "stage.batch_count=expected_batch_count", "generation.state IN ('building','evaluating')", "schema_version=8"} {
		if !containsSQL(string(stageContents), fragment) {
			t.Errorf("resumable-stage migration lacks %q", fragment)
		}
	}
	blockContents, err := Files.ReadFile("009_generation_storage_blocks.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"generation_storage_blocks", "WHERE cleared_at IS NULL", "cleared_by text", "required_free_bytes bigint", "schema_version=9"} {
		if !containsSQL(string(blockContents), fragment) {
			t.Errorf("storage-block migration lacks %q", fragment)
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
		if !containsSQL(sql, fragment) {
			t.Errorf("schema-4 migration is missing contract fragment %q", fragment)
		}
	}
	for _, forbidden := range []string{"INSERT INTO osm_canonical.ways\n         SELECT", "DELETE FROM osm_canonical.ways"} {
		if containsSQL(sql, forbidden) {
			t.Errorf("schema-4 migration contains copying promotion fragment %q", forbidden)
		}
	}
	for _, fragment := range []string{"FULL JOIN %1$I.logical_paths stored", "IS DISTINCT FROM", "abs(derived.member_length_m-stored.member_length_m) > 0.01"} {
		if !containsSQL(sql, fragment) {
			t.Errorf("schema-4 promotion source is missing tolerant logical-path check %q", fragment)
		}
	}
	if containsSQL(sql, "EXCEPT SELECT * FROM %1$I.logical_paths") {
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
		if !containsSQL(sql, fragment) {
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
		if !containsSQL(sql, fragment) {
			t.Errorf("partition validation is missing gate %q", fragment)
		}
	}
	for _, fragment := range []string{"validation_logical_keys", "LEFT JOIN LATERAL", "IS DISTINCT FROM", "abs(derived.member_length_m-stored.member_length_m) > 0.01", "LIMIT logical_batch_size"} {
		if !containsSQL(sql, fragment) {
			t.Errorf("partition validation is missing tolerant logical-path check %q", fragment)
		}
	}
	if containsSQL(sql, "EXCEPT SELECT") {
		t.Error("partition validation still contains an exact EXCEPT comparison")
	}
}

func TestTolerantLogicalPathPromotionMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("005_tolerant_logical_path_promotion.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	parts := strings.Split(sql, "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("schema-5 migration must have exactly one Goose Down section")
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
		"UPDATE osm_catalog.schema_metadata SET schema_version = 5",
	} {
		if !containsSQL(up, fragment) {
			t.Errorf("schema-5 migration Up is missing %q", fragment)
		}
	}
	if containsSQL(up, "EXCEPT SELECT") {
		t.Error("effective schema-6 promotion still contains exact logical-path EXCEPT comparison")
	}
	for _, forbidden := range []string{"pg_get_functiondef", "regexp_replace", "replace(definition", "EXECUTE replace"} {
		if containsSQL(sql, forbidden) {
			t.Errorf("schema-6 migration dynamically rewrites a function definition with %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION osm_catalog.promote_region_generation(",
		"EXCEPT SELECT * FROM %1$I.logical_paths",
		"SELECT * FROM %1$I.logical_paths EXCEPT",
		"INSERT INTO osm_catalog.storage_gc",
		"INSERT INTO osm_catalog.region_storage",
		"UPDATE osm_catalog.schema_metadata SET schema_version = 4",
	} {
		if !containsSQL(down, fragment) {
			t.Errorf("schema-5 migration Down does not restore schema 4 fragment %q", fragment)
		}
	}
}

func TestOutsideLogicalPathsAreProviderRegionScoped(t *testing.T) {
	for _, path := range []string{
		"../../../osm/derive-batched.sql",
		"../../../osm/clip-localities-replacements.sql",
		"../../../osm/clip-localities-residual.sql",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(contents)
		for _, required := range []string{"workouts_explorer.osm_region_id", "osm-logical-path/v2:region:"} {
			if !containsSQL(sql, required) {
				t.Errorf("%s is missing %q", path, required)
			}
		}
		if containsSQL(sql, "osm-logical-path/v1:outside:") {
			t.Errorf("%s still contains globally merged outside logical paths", path)
		}
	}
}

func TestPromotionEventsMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("006_promotion_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{
		"CREATE TABLE osm_catalog.promotion_events", "UNIQUE (region_id,generation_id)",
		"CREATE TRIGGER generations_record_promotion_event", "NEW.state='active'",
		"CREATE FUNCTION osm_catalog.read_promotion_events", "ORDER BY event.id",
		"CREATE FUNCTION osm_catalog.promotion_event_head",
		"schema_version=6,minimum_runtime_version=5",
	} {
		if !containsSQL(sql, required) {
			t.Errorf("promotion event migration missing %q", required)
		}
	}
}

func TestConnectedAttributionPromotionMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("007_connected_attribution_promotion.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"RENAME TO promote_region_generation_v7",
		"'remainingConnectedAttributionSplits',0",
		"PERFORM osm_catalog.promote_region_generation_v7",
		"schema_version=7,minimum_runtime_version=7",
	} {
		if !containsSQL(string(contents), required) {
			t.Errorf("connected attribution promotion migration missing %q", required)
		}
	}
}

func TestParkAttributionDerivationContract(t *testing.T) {
	for name, fragments := range map[string][]string{
		"../../../osm/import.lua": {"park_ways", "park_relations", "object.is_closed", "object:as_polygon()",
			"tags.boundary == 'national_park'", "tags.protected_area == 'national_park'", "string.lower(protection_title)",
			"education_ways", "education_relations", "tags.amenity == 'school'", "tags.amenity == 'university'", "tags.landuse == 'education'"},
		"../../../osm/postprocess.sql": {"park_areas", "'national_park'", "'state_park'", "protection_title'))='state park'",
			"area_m2 BETWEEN 500 AND 25000000", "area_m2 BETWEEN 1000000 AND 100000000000",
			"park_kind IN ('state_park','national_park') OR tags->>'protect_class' IS DISTINCT FROM '2'",
			"WHEN 'state_park' THEN 1 WHEN 'national_park' THEN 1", "admin_level' IN ('6','8')",
			"education_areas", "education_kind='school'", "education_kind='university'", "area_m2 BETWEEN 5000 AND 500000000"},
		"../../../osm/attribute-parks-batched.sql": {"ST_Difference(segment.geom,candidate.geom)", "workouts:park_id",
			"locality.admin_level=8", "workouts:park_kind", "workouts-explorer/osm-park/v1",
			"workouts-explorer/osm-state-park/v1", "workouts-explorer/osm-national-park/v1", "ORDER BY candidate.type_priority"},
		"../../../osm/attribute-education-batched.sql": {"ST_Difference(segment.geom,candidate.geom)",
			"workouts:education_id", "workouts:education_name", "workouts-explorer/osm-education/v1:"},
		"../../../osm/attribute-slivers-batched.sql": {"county.admin_level=6", "middle.length_m<=25"},
		"../../../osm/identity-segments-batched.sql": {"workouts-explorer/osm-attribution-group/v3:", "WHEN segment.normalized_name IS NOT NULL THEN 'named'",
			"lower(regexp_replace(btrim(segment.name)"},
		"../../../osm/identity-components.sql":              {"attribution_identity_branch_nodes", "attribution_identity_branch_classes", "AND edge.unnamed", "edge.left_source_way_id<>edge.right_source_way_id"},
		"../../../osm/identity-proximity-batched.sql":       {"workouts-explorer/osm-named-road-proximity/v1:", "ST_DWithin(a.geom::geography,candidate.geom::geography,50)", "a.oneway AND candidate.oneway"},
		"../../../osm/identity-propagate-batched.sql":       {"parent_segment_id", "workouts-explorer/osm-logical-path/v12:group:", "remaining_connected_splits"},
		"../../../osm/identity-label-overrides-batched.sql": {"attribution_identity_component_overrides", "length_m<=25", "HAVING count(DISTINCT (replacement_group_id,replacement_parent_segment_id))=1"},
		"../../../osm/identity-rewrite-batched.sql": {"DROP TABLE path_segments", "ALTER TABLE path_segments_rewritten RENAME TO path_segments",
			"workouts-explorer/osm-logical-path/v14:", "label_override.replacement_name"},
		"../../../osm/validate.sql": {"qualifyingParkAreas", "qualifyingNationalParkAreas", "invalidNationalParkAreas",
			"qualifyingStateParkAreas", "invalidStateParkAreas", "stateParkAttributedSegments",
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
			if !containsSQL(string(contents), fragment) {
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
		if !containsSQL(string(contents), fragment) {
			t.Errorf("connected attribution fixture missing %q", fragment)
		}
	}
}

func TestConnectedAttributionIdentityUsesIndexedTopologyAndClass(t *testing.T) {
	var sql strings.Builder
	for _, path := range []string{
		"../../../osm/identity-segments-batched.sql",
		"../../../osm/identity-edges-batched.sql",
		"../../../osm/identity-components.sql",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sql.Write(contents)
	}
	derivation := sql.String()
	if !containsSQL(derivation, "WHEN segment.normalized_name IS NOT NULL THEN 'named'") ||
		!containsSQL(derivation, "WHEN segment.broad_class='road' THEN 'road'") {
		t.Error("connected attribution group does not preserve named bridging and unnamed road/path boundaries")
	}
	for _, required := range []string{
		"group_id uuid NOT NULL",
		"b.start_graph_node_id=a.start_graph_node_id",
		"b.end_graph_node_id=a.start_graph_node_id",
		"b.end_graph_node_id=a.end_graph_node_id",
		"CREATE TEMP TABLE identity_edge_keys",
		"LIMIT batch_size",
		"SET LOCAL enable_hashjoin = off", "SET LOCAL enable_mergejoin = off",
	} {
		if !containsSQL(derivation, required) {
			t.Errorf("connected attribution derivation missing bounded join contract %q", required)
		}
	}
	if containsSQL(derivation, "b.start_graph_node_id=a.end_graph_node_id") {
		t.Error("identity edges retain redundant symmetric end/start orientation")
	}
	componentStart := strings.Index(derivation, "CREATE TABLE attribution_identity_components")
	if componentStart < 0 {
		t.Fatal("connected attribution component table is missing")
	}
	edgeDefinition := derivation[:componentStart]
	if containsSQL(edgeDefinition, "scope_kind text") || containsSQL(edgeDefinition, "name_key text") {
		t.Error("cross-logical-ID edges repeat scope/name text instead of using the compact group UUID")
	}
}

func TestConnectedAttributionValidationUsesRecordedRequiredEdges(t *testing.T) {
	contents, err := os.ReadFile("../../../osm/validate.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	if !containsSQL(sql, "SELECT to_jsonb(remaining_connected_splits) FROM attribution_identity_stats") {
		t.Error("validation does not use the exact required-edge split count recorded during derivation")
	}
	if containsSQL(sql, "b.start_graph_node_id=a.start_graph_node_id") {
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
		if !containsSQL(string(contents), fragment) {
			t.Errorf("selective filter is missing %q", fragment)
		}
	}
}

func TestMatcherEndpointIndexesArePartOfPartitionMigration(t *testing.T) {
	contents, err := Files.ReadFile("004_storage_bounded_partitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, fragment := range []string{
		"canonical_path_segments_start_graph_node_idx\nON osm_canonical.path_segments (start_graph_node_id)",
		"canonical_path_segments_end_graph_node_idx\nON osm_canonical.path_segments (end_graph_node_id)",
	} {
		if !containsSQL(sql, fragment) {
			t.Errorf("schema-4 migration is missing endpoint-index fragment %q", fragment)
		}
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
		if !containsSQL(sql, invariant) {
			t.Errorf("overlap fixture is missing invariant %q", invariant)
		}
	}
}
