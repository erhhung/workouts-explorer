package database

import (
	"crypto/md5"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/coverage"
)

func TestRouteGapReadinessAndSharedSpatialLimit(t *testing.T) {
	if SupportedSchemaVersion != 31 || !strings.HasSuffix(runtimeFullReadinessSQL, " AND ("+runtimeRouteGapReadinessSQL+")") {
		t.Fatal("missing route gap readiness")
	}
	sql, err := migrations.Files.ReadFile("009_raw_route_map.sql")
	if err != nil {
		t.Fatal(err)
	}
	temporal, err := migrations.Files.ReadFile("010_segment_route_geometry.sql")
	if err != nil {
		t.Fatal(err)
	}
	var definitions [][]string
	for _, definition := range regexp.MustCompile(`(?s)CREATE(?: OR REPLACE)? FUNCTION app\.([a-z_]+).*?AS \$function\$(.*?)\$function\$;`).FindAllStringSubmatch(string(sql)+"\n"+string(temporal), -1) {
		if definition[1] == "split_raw_route_spatial_gaps" || definition[1] == "build_segmented_workout_route" {
			definitions = append(definitions, definition)
		}
	}
	if len(definitions) != 2 {
		t.Fatal("unexpected geometry policy contract")
	}
	for _, definition := range definitions {
		if !strings.Contains(runtimeRouteGapReadinessSQL, fmt.Sprintf("%x", md5.Sum([]byte(definition[2])))) {
			t.Fatal("unverified function body: " + definition[1])
		}
	}
	source, err := os.ReadFile("../../ui/src/MapPage.tsx")
	if err != nil {
		t.Fatal(err)
	}
	limit := regexp.MustCompile(`RAW_ROUTE_MAX_GAP_METERS = (\d+)`).FindSubmatch(source)
	if len(limit) != 2 {
		t.Fatal("missing raw route limit")
	}
	value, err := strconv.Atoi(string(limit[1]))
	if err != nil || float64(value) != coverage.ExperimentalRules().MaxSpatialGapMeters || !strings.Contains(string(sql), fmt.Sprintf("> %d THEN", value)) {
		t.Fatal("raw SQL, UI and Coverage spatial limits differ")
	}
}
