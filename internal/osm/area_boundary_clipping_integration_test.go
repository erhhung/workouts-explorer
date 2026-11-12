package osm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAreaBoundaryClippingDisposableIntegration(t *testing.T) {
	url := os.Getenv("OSM_AREA_CLIPPING_TEST_URL")
	if url == "" || os.Getenv("OSM_AREA_CLIPPING_DISPOSABLE") != "true" {
		t.Skip("requires OSM_AREA_CLIPPING_TEST_URL and OSM_AREA_CLIPPING_DISPOSABLE=true on an owned disposable PostGIS server")
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1" && config.Host != "/tmp/opencode/tile-local031-rehearsal/socket" {
		t.Fatal("area clipping fixtures require an explicitly disposable local server")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			t.Fatal("alternate database endpoints are not allowed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	name := fmt.Sprintf("test_osm_area_clipping_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	config.Database = name
	environment := []string{"PGHOST=" + config.Host, "PGPORT=" + strconv.Itoa(int(config.Port)),
		"PGUSER=" + config.User, "PGPASSWORD=" + config.Password, "PGDATABASE=" + name,
		"PGSSLMODE=disable", "PGOPTIONS=-c statement_timeout=30000 -c lock_timeout=2000"}
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if _, err := db.Exec(ctx, `CREATE EXTENSION postgis;
CREATE SCHEMA osm_catalog;
CREATE TABLE osm_catalog.generations(id bigint PRIMARY KEY,state text);
INSERT INTO osm_catalog.generations VALUES(1,'building');
CREATE TABLE osm_catalog.schema_metadata(singleton boolean,schema_version integer,minimum_runtime_version integer);`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/008_resumable_generation_stages.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../../osm/testdata/area-boundary-clipping.sql")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-f", fixture)
	command.Env = append(os.Environ(), environment...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("area boundary regression failed: %v\n%s", err, output)
	}
	poolConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := PostgreSQLUpdateStore{Pool: pool}
	oldFence, newFence := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, stage := range []string{"validate", "clip-replacements"} {
		if _, err := db.Exec(ctx, `DELETE FROM osm_catalog.generation_stages WHERE generation_id=1 AND stage=$1`, stage); err != nil {
			t.Fatal(err)
		}
		if err := store.StartStage(ctx, 1, stage, 0, oldFence); err != nil {
			t.Fatal(err)
		}
		if err := store.StartStage(ctx, 1, stage, 0, newFence); err == nil {
			t.Fatalf("running %s accepted a changed fence", stage)
		}
		if _, err := db.Exec(ctx, `UPDATE osm_catalog.generation_stages SET state='completed',batch_count=7,rows_processed=99,cursor='{"phase":"done"}' WHERE generation_id=1 AND stage=$1`, stage); err != nil {
			t.Fatal(err)
		}
		err := store.StartStage(ctx, 1, stage, 0, newFence)
		if stage != "validate" {
			if err == nil {
				t.Fatal("completed geometry stage accepted a changed fence")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var reset bool
		if err := db.QueryRow(ctx, `SELECT state='running' AND fence=$1 AND batch_count=0 AND rows_processed=0 AND cursor='{}' AND checkpointed_at IS NULL FROM osm_catalog.generation_stages WHERE generation_id=1 AND stage='validate'`, newFence).Scan(&reset); err != nil || !reset {
			t.Fatalf("validation must restart fully under the corrected observational rules: reset=%t error=%v", reset, err)
		}
	}
}
