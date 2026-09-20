package migrations

import (
	"strings"
	"testing"
)

func TestTimezoneMigrationLookupAndPromotionContract(t *testing.T) {
	contents, err := Files.ReadFile("002_timezone_boundaries.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	required := []string{
		"geometry(MultiPolygon, 4326)",
		"USING gist (boundary)",
		"ST_Covers(geometry.boundary, point.geom)",
		"ORDER BY ST_Area(geometry.boundary::geography), geometry.tzid, geometry.id",
		"LOCK TABLE osm_catalog.timezone_datasets",
		"state = 'retired'",
		"DELETE FROM osm_catalog.timezone_geometries",
		"source_sha256",
		"boundary_count",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration is missing contract fragment %q", fragment)
		}
	}
}
