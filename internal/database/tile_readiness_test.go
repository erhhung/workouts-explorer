package database

import (
	"crypto/md5"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestTileReadinessContract(t *testing.T) {
	if SupportedSchemaVersion != 31 || !strings.Contains(runtimeFullReadinessSQL, " AND ("+runtimeTileReadinessSQL+")") {
		t.Fatal("current runtime must enforce the tile contract")
	}
	for _, required := range []string{"SELECT $1 < 31 OR (", "minimum_runtime_version >= 31", "migration_baseline = 27",
		"app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean)", "timestamp with time zone", "md5(p.prosrc)<>expected.body_hash",
		"NOT c.relrowsecurity OR NOT c.relforcerowsecurity", "256", "gist_geometry_ops_2d",
		"has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api",
		"has_function_privilege('workouts_tiles',p.oid,'EXECUTE')<>expected.tiles",
		"aclexplode(a.attacl)", "k.convalidated AND k.conenforced", "t.tgtype<>29", "t.tgenabled<>'O'"} {
		if !strings.Contains(runtimeTileReadinessSQL, required) {
			t.Errorf("missing tile catalog guard %q", required)
		}
	}
	for _, forbidden := range []string{"FROM app.raw_route_tile_chunks", "FROM app.coverage_tile_chunks", "statement_timeout"} {
		if strings.Contains(runtimeTileReadinessSQL, forbidden) {
			t.Errorf("readiness must remain catalog-only: %s", forbidden)
		}
	}
}

func TestTileReadinessExactBodies(t *testing.T) {
	raw, err := migrations.Files.ReadFile("031_tile_rendering.sql")
	if err != nil {
		t.Fatal(err)
	}
	functions := canonicalDisplayFunctions(string(raw))
	fingerprints := regexp.MustCompile(`(?s)\('app\.([a-z_]+)\([^']*\)'.*?,'([a-f0-9]{32})'\)`).FindAllStringSubmatch(runtimeTileReadinessSQL, -1)
	if len(functions) != 10 || len(fingerprints) != 7 {
		t.Fatalf("tile function inventory migration=%d readiness=%d", len(functions), len(fingerprints))
	}
	checked := 0
	for _, function := range functions {
		want := fmt.Sprintf("%x", md5.Sum([]byte(function[4])))
		for _, fingerprint := range fingerprints {
			if fingerprint[1] == function[1] {
				checked++
				if fingerprint[2] != want {
					t.Errorf("wrong fingerprint for %s: %s", function[1], want)
				}
			}
		}
	}
	if checked != len(fingerprints) {
		t.Fatal("missing current tile function fingerprints")
	}
}

func TestTileRuntimeAdvancesOnlyPhaseFence(t *testing.T) {
	raw, err := migrations.Files.ReadFile("029_ingest_status_reporting.sql")
	if err != nil {
		t.Fatal(err)
	}
	var original string
	for _, function := range canonicalDisplayFunctions(string(raw)) {
		if function[1] == "record_ingest_phase" {
			original = function[4]
		}
	}
	if original == "" {
		t.Fatal("missing original phase writer")
	}
	patch, err := migrations.Files.ReadFile("031_tile_rendering.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), fmt.Sprintf("%x", md5.Sum([]byte(original)))) {
		t.Fatal("phase fence patch must reject an unexpected source body")
	}
	current := strings.NewReplacer("minimum_runtime_version <= 30", "minimum_runtime_version <= 31",
		"ingest phase runtime 30 is required", "ingest phase runtime 31 is required",
		"fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 30)",
		"fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 31)").Replace(original)
	if !strings.Contains(runtimeTileReadinessSQL, fmt.Sprintf("%x", md5.Sum([]byte(current)))) {
		t.Fatal("runtime31 must fingerprint the effective phase writer")
	}
}
