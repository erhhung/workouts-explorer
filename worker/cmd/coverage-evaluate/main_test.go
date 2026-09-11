package main

import "testing"

func TestLoadConfigDefaultsAndBounds(t *testing.T) {
	base := map[string]string{
		"MIGRATION_DATABASE_URL":      "postgres://migration.invalid/db",
		"OSM_DATABASE_URL":            "postgres://osm.invalid/db",
		"COVERAGE_EVALUATION_ACCOUNT": "canonical-user",
	}
	getenv := func(key string) string { return base[key] }
	cfg, err := loadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.evaluation.Limit != 20 || cfg.evaluation.MinimumTraversalMeters != 5 {
		t.Fatalf("defaults = %+v", cfg.evaluation)
	}

	tests := []struct {
		name, key, value string
	}{
		{"zero limit", "COVERAGE_EVALUATION_LIMIT", "0"},
		{"large limit", "COVERAGE_EVALUATION_LIMIT", "501"},
		{"invalid limit", "COVERAGE_EVALUATION_LIMIT", "many"},
		{"small traversal", "COVERAGE_MIN_TRAVERSAL_METERS", "0.09"},
		{"large traversal", "COVERAGE_MIN_TRAVERSAL_METERS", "100.1"},
		{"nan traversal", "COVERAGE_MIN_TRAVERSAL_METERS", "NaN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range base {
				values[key] = value
			}
			values[test.key] = test.value
			if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadConfigAcceptsInclusiveBounds(t *testing.T) {
	values := map[string]string{
		"MIGRATION_DATABASE_URL":        "postgres://migration.invalid/db",
		"OSM_DATABASE_URL":              "postgres://osm.invalid/db",
		"COVERAGE_EVALUATION_ACCOUNT":   "canonical@example.invalid",
		"COVERAGE_EVALUATION_LIMIT":     "500",
		"COVERAGE_MIN_TRAVERSAL_METERS": "0.1",
	}
	cfg, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.evaluation.Limit != 500 || cfg.evaluation.MinimumTraversalMeters != 0.1 {
		t.Fatalf("configured values = %+v", cfg.evaluation)
	}
}

func TestLoadConfigRequiresConnectionsAndAccount(t *testing.T) {
	if _, err := loadConfig(func(string) string { return "" }); err == nil {
		t.Fatal("expected required configuration error")
	}
}
