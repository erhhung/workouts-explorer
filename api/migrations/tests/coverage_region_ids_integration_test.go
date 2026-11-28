package migrations_test

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

//go:embed testdata/geofabrik-region-ids.json
var geofabrikRegionIDs []byte

func TestAllGeofabrikCoverageRegionIDsIntegration(t *testing.T) {
	db := openTestDatabases(t)
	var fixture struct {
		CatalogIDs    []string `json:"catalogIDs"`
		OtherKnownIDs []string `json:"otherKnownIDs"`
	}
	if err := json.Unmarshal(geofabrikRegionIDs, &fixture); err != nil {
		t.Fatal(err)
	}
	ids := append(fixture.CatalogIDs, fixture.OtherKnownIDs...)
	encoded, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.migration.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(db.ctx)
	var rejected []string
	if err := tx.QueryRow(db.ctx, `SELECT COALESCE(array_agg(id),'{}') FROM jsonb_array_elements_text($1) source(id)
		WHERE NOT app.valid_coverage_generation_vector(jsonb_build_array(jsonb_build_object('regionId',id,'generation',1)))`, encoded).Scan(&rejected); err != nil {
		t.Fatal(err)
	}
	if len(rejected) > 0 {
		t.Fatalf("%d public region IDs rejected: %v", len(rejected), rejected)
	}
	if _, err := tx.Exec(db.ctx, `CREATE TEMP TABLE coverage_region_id_probe(region_id text NOT NULL) ON COMMIT DROP`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(db.ctx, `SELECT conname,pg_get_constraintdef(oid) FROM pg_constraint
		WHERE connamespace='app'::regnamespace AND conname IN('coverage_diagnostic_generations_region_id_check',
		'coverage_job_contexts_region_id_check','coverage_region_catalog_region_id_check',
		'path_segments_region_id_check','workout_coverage_regions_region_id_check') ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	type check struct{ name, definition string }
	var checks []check
	for rows.Next() {
		var value check
		if err := rows.Scan(&value.name, &value.definition); err != nil {
			t.Fatal(err)
		}
		checks = append(checks, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(checks) != 5 {
		t.Fatal("missing region identifier constraints", err, len(checks))
	}
	for _, value := range checks {
		if _, err := tx.Exec(db.ctx, fmt.Sprintf(`ALTER TABLE coverage_region_id_probe ADD CONSTRAINT %s %s`, pgx.Identifier{value.name}.Sanitize(), value.definition)); err != nil {
			t.Fatal(err)
		}
	}
	if inserted, err := tx.Exec(db.ctx, `INSERT INTO coverage_region_id_probe SELECT value FROM jsonb_array_elements_text($1)`, encoded); err != nil || inserted.RowsAffected() != int64(len(ids)) {
		t.Fatal("public identifiers failed actual region constraints", err, inserted.RowsAffected())
	}
	catalogTx, err := db.coverage.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogTx.Rollback(db.ctx)
	// Keep the existing per-call catalog limit; every ID is checked in bounded batches.
	for start := 0; start < len(ids); start += 256 {
		var batch []map[string]string
		for _, id := range ids[start:min(start+256, len(ids))] {
			batch = append(batch, map[string]string{"regionId": id, "displayName": "Catalog audit"})
		}
		body, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		var changed int
		if err := catalogTx.QueryRow(db.ctx, `SELECT app.sync_coverage_region_catalog($1)`, body).Scan(&changed); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("Accepted %d current catalog IDs and %d cached legacy IDs through generation validation, all five region constraints and restricted-role catalog synchronization", len(fixture.CatalogIDs), len(fixture.OtherKnownIDs))
}

func TestHierarchicalCoverageRegionIDsIntegration(t *testing.T) {
	db := openTestDatabases(t)
	for _, test := range []struct {
		id    string
		valid bool
	}{
		{"geofabrik:norcal", true},
		{"geofabrik:us/nevada", true},
		{"provider:country/state/city", true},
		{strings.Repeat("p", 64) + ":" + strings.Repeat("r", 192), true},
		{strings.Repeat("p", 65) + ":region", false},
		{"provider:" + strings.Repeat("r", 193), false},
		{"provider:", false},
		{"provider:/nevada", false},
		{"provider:us/../nevada", false},
		{"provider:us/nevada other", false},
		{"Provider:us/nevada", false},
	} {
		t.Run(test.id, func(t *testing.T) {
			vector, err := json.Marshal([]map[string]any{{"regionId": test.id, "generation": 1}})
			if err != nil {
				t.Fatal(err)
			}
			var valid bool
			if err := db.migration.QueryRow(db.ctx, `SELECT app.valid_coverage_generation_vector($1::jsonb)`, vector).Scan(&valid); err != nil {
				t.Fatal(err)
			}
			if valid != test.valid {
				t.Fatalf("region ID validation=%t; want %t", valid, test.valid)
			}
		})
	}
}
