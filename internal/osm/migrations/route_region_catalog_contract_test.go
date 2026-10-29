package migrations

import (
	"strings"
	"testing"
)

func TestRouteRegionCatalogMigrationContract(t *testing.T) {
	contents, err := Files.ReadFile("010_route_region_catalog_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(contents), "-- +goose Down")
	if len(parts) != 2 {
		t.Fatal("expected one forward-only Down")
	}
	up := parts[0]
	for _, required := range []string{
		"boundary_sha256 bytea GENERATED ALWAYS AS (pg_catalog.sha256(public.ST_AsEWKB(boundary, 'NDR'))) STORED NOT NULL",
		"geographic_area_m2 double precision GENERATED ALWAYS AS (public.ST_Area(boundary::public.geography)) STORED NOT NULL",
		"singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton)", "revision bigint NOT NULL CHECK (revision >= 1)",
		"route-region-catalog-v1", "route-region-selection-v1", "int8send(COALESCE(generation.id, 0))",
		"boolsend(region.configured AND generation.id IS NOT NULL)", "ORDER BY region.id COLLATE \"C\"",
		"revision = revision + 1", "IS DISTINCT FROM (full_proof, selection_proof, heads, configured_names, names)",
		"BEFORE INSERT OR DELETE OR UPDATE OF id, boundary, configured, display_name ON osm_catalog.regions",
		"AFTER INSERT OR DELETE OR UPDATE OF id, boundary, configured, display_name ON osm_catalog.regions",
		"BEFORE INSERT OR DELETE OR UPDATE OF id, region_id, state ON osm_catalog.generations",
		"AFTER INSERT OR DELETE OR UPDATE OF id, region_id, state ON osm_catalog.generations",
		"FOR EACH STATEMENT", "BEFORE TRUNCATE ON osm_catalog.regions", "BEFORE TRUNCATE ON osm_catalog.generations",
		"BEFORE TRUNCATE ON osm_catalog.route_region_catalog_state", "FOR UPDATE",
		"REVOKE ALL ON osm_catalog.route_region_catalog_state FROM PUBLIC",
		"REVOKE ALL ON FUNCTION osm_catalog.refresh_route_region_catalog() FROM PUBLIC",
		"REVOKE ALL ON FUNCTION osm_catalog.lock_route_region_catalog() FROM PUBLIC",
		"SECURITY DEFINER SET search_path = pg_catalog, osm_catalog", "aclexplode", "acl.grantee <> 0",
		"ALTER TABLE osm_catalog.route_region_catalog_state OWNER TO %I",
		"ALTER FUNCTION osm_catalog.refresh_route_region_catalog() OWNER TO %I",
		"GRANT SELECT ON osm_catalog.route_region_catalog_state TO %I",
		"schema_version = 10, minimum_runtime_version = 10",
	} {
		if !containsSQL(up, required) {
			t.Errorf("migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"nextval(", "CREATE SEQUENCE", "FOR EACH ROW", "UPDATE OF updated_at", "GRANT ALL", "GRANT EXECUTE"} {
		if containsSQL(up, forbidden) {
			t.Errorf("migration contains %q", forbidden)
		}
	}
	refresh := up[strings.Index(up, "CREATE FUNCTION osm_catalog.refresh_route_region_catalog()"):]
	if strings.Index(refresh, "FOR UPDATE") > strings.Index(refresh, "SELECT sha256") {
		t.Fatal("fingerprints must be computed after acquiring the singleton lock")
	}
	if !strings.Contains(parts[1], "RAISE EXCEPTION 'OSM schema 10 is forward-only") {
		t.Fatal("Down may not reset published revision proofs")
	}
}
