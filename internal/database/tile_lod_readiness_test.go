package database

import (
	"crypto/md5"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestDenseTileReadinessContract(t *testing.T) {
	if SupportedSchemaVersion != 31 || !strings.Contains(runtimeFullReadinessSQL, " AND ("+runtimeTileLODReadinessSQL+")") {
		t.Fatal("current runtime must compose LOD readiness")
	}
	for _, required := range []string{"SELECT $1 < 31 OR (", "minimum_runtime_version >= 31", "migration_baseline = 27",
		"c.relrowsecurity AND c.relforcerowsecurity", "aclexplode(a.attacl)", "k.convalidated AND k.conenforced",
		"gist_geometry_ops_2d", "t.tgtype=29", "t.tgenabled='O'", "work_mem=32MB", "md5(p.prosrc)<>expected.body_hash"} {
		if !strings.Contains(runtimeTileLODReadinessSQL, required) {
			t.Errorf("missing additive catalog guard %q", required)
		}
	}
	if strings.Contains(runtimeTileLODReadinessSQL, "FROM app.raw_route_lod_chunks") {
		t.Fatal("readiness must not read private render data")
	}
	raw, err := migrations.Files.ReadFile("031_tile_rendering.sql")
	if err != nil {
		t.Fatal(err)
	}
	functions := canonicalDisplayFunctions(string(raw))
	fingerprints := regexp.MustCompile(`(?s)\('app\.([a-z_0-9]+)\([^']*\)'.*?,'([a-f0-9]{32})'\)`).FindAllStringSubmatch(runtimeTileLODReadinessSQL, -1)
	if len(functions) != 10 || len(fingerprints) != 5 {
		t.Fatalf("tile inventory migration=%d readiness=%d", len(functions), len(fingerprints))
	}
	checked := 0
	for _, function := range functions {
		want := fmt.Sprintf("%x", md5.Sum([]byte(function[4])))
		for _, fingerprint := range fingerprints {
			if fingerprint[1] == function[1] {
				checked++
				if fingerprint[2] != want {
					t.Errorf("wrong LOD fingerprint for %s: %s", function[1], want)
				}
			}
		}
	}
	if checked != len(fingerprints) {
		t.Fatal("missing LOD function fingerprints")
	}
}
