package osm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetainedBuildStorageAfterCleanupIntegration(t *testing.T) {
	url := os.Getenv("OSM_BUILD_USAGE_TEST_URL")
	if url == "" || os.Getenv("OSM_BUILD_USAGE_DISPOSABLE") != "1" {
		t.Skip("requires owned disposable OSM database")
	}
	configuration, err := pgxpool.ParseConfig(url)
	if err != nil || configuration.ConnConfig.Host != "127.0.0.1" || !strings.HasPrefix(configuration.ConnConfig.Database, "acceptance_test_osm") {
		t.Fatal("disposable loopback OSM database required")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	region := "fixture:usage-" + uuid.NewString()[:8]
	_, err = pool.Exec(ctx, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary)
        VALUES($1,'fixture',$2,'Build usage','https://example.test','https://example.test/region.pbf',ST_Multi(ST_MakeEnvelope(0,0,1,1,4326)))`, region, strings.TrimPrefix(region, "fixture:"))
	if err != nil {
		t.Fatal(err)
	}
	store := PostgreSQLUpdateStore{Pool: pool}
	generation, err := store.Reserve(ctx, region, ToolVersions{Osmium: "fixture", Osm2pgsql: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{generation.SchemaName}.Sanitize()+" CASCADE")
		_, _ = pool.Exec(ctx, `DELETE FROM osm_catalog.generations WHERE id=$1`, generation.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM osm_catalog.regions WHERE id=$1`, region)
	}()
	schema := pgx.Identifier{generation.SchemaName}.Sanitize()
	_, err = pool.Exec(ctx, `CREATE TABLE `+schema+`.retained(id bigint,payload text);INSERT INTO `+schema+`.retained SELECT n,md5(n::text) FROM generate_series(1,10000) n;CREATE INDEX retained_lookup ON `+schema+`.retained(id);
        CREATE TABLE `+schema+`.batch_scratch AS SELECT * FROM `+schema+`.retained;DROP TABLE `+schema+`.batch_scratch;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecordBuildDatabaseSize(ctx, generation); err != nil {
		t.Fatal(err)
	}
	var actual, expected int64
	var at time.Time
	err = pool.QueryRow(ctx, `SELECT build_database_bytes,build_database_measured_at,pg_total_relation_size($2::regclass) FROM osm_catalog.generations WHERE id=$1`, generation.ID, generation.SchemaName+".retained").Scan(&actual, &at, &expected)
	if err != nil || actual != expected || actual <= 0 || at.IsZero() {
		t.Fatalf("physical accounting: actual=%d expected=%d err=%v", actual, expected, err)
	}
	regions, err := CatalogRegions(ctx, pool, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range regions {
		if item.ID == region {
			found = item.BuildDatabaseBytes != nil && *item.BuildDatabaseBytes == actual && item.BuildDatabaseID != nil && *item.BuildDatabaseID == generation.ID
		}
	}
	if !found {
		t.Fatal("snapshot was not exposed by catalog")
	}
	if _, err = pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	regions, err = CatalogRegions(ctx, pool, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range regions {
		if item.ID == region && item.BuildDatabaseBytes != nil {
			t.Fatal("dropped candidate retained a visible size")
		}
	}
}
