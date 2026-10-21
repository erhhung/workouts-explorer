package osm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This test creates its own database, and never uses OSM_DATABASE_URL or any of
// the application's migration URLs. The supplied server must itself be disposable.
func TestUpdateStatisticsDisposableIntegration(t *testing.T) {
	url := os.Getenv("OSM_UPDATE_STATISTICS_TEST_URL")
	if url == "" || os.Getenv("OSM_UPDATE_STATISTICS_DISPOSABLE") != "true" {
		t.Skip("requires OSM_UPDATE_STATISTICS_TEST_URL and OSM_UPDATE_STATISTICS_DISPOSABLE=true for a disposable PostgreSQL 17+ server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("osm_stats_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		if _, err := admin.Exec(cleanup, "DROP ROLE IF EXISTS "+identifier); err != nil {
			t.Error(err)
		}
	})
	adminConfig, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.Database = name
	db, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if _, err := db.Exec(ctx, `CREATE SCHEMA osm_catalog; CREATE SCHEMA osm_canonical;
CREATE TABLE osm_catalog.generations(id bigint PRIMARY KEY,region_id text,state text);
INSERT INTO osm_catalog.generations VALUES(42,'fixture:a','active'),(43,'fixture:b','active');
CREATE TABLE osm_canonical.ways(region_id text,generation_id bigint,way_id bigint,version integer) PARTITION BY LIST(region_id);
CREATE TABLE osm_canonical.localities(region_id text,generation_id bigint,relation_id bigint,relation_version integer) PARTITION BY LIST(region_id);
CREATE TABLE osm_canonical.path_segments(region_id text,generation_id bigint,segment_id bigint,source_way_id bigint,source_way_version integer,logical_path_id bigint,start_graph_node_id uuid,end_graph_node_id uuid) PARTITION BY LIST(region_id);
CREATE TABLE osm_canonical.ways_g42 (LIKE osm_canonical.ways);
CREATE TABLE osm_canonical.ways_g43 (LIKE osm_canonical.ways);
CREATE TABLE osm_canonical.localities_g42 (LIKE osm_canonical.localities);
CREATE TABLE osm_canonical.localities_g43 (LIKE osm_canonical.localities);
CREATE TABLE osm_canonical.path_segments_g42 (LIKE osm_canonical.path_segments);
CREATE TABLE osm_canonical.path_segments_g43 (LIKE osm_canonical.path_segments);
INSERT INTO osm_canonical.ways_g42 SELECT 'fixture:a',42,n,1 FROM generate_series(1,100) n;
INSERT INTO osm_canonical.ways_g43 SELECT 'fixture:b',43,n,2 FROM generate_series(1,100) n;
INSERT INTO osm_canonical.localities_g42 SELECT 'fixture:a',42,n,1 FROM generate_series(1,10) n;
INSERT INTO osm_canonical.localities_g43 SELECT 'fixture:b',43,n,2 FROM generate_series(1,10) n;
INSERT INTO osm_canonical.path_segments_g42 SELECT 'fixture:a',42,n,n,1,n,md5('a-node:'||(n/3)::text)::uuid,md5('a-node:'||((n+1)/3)::text)::uuid FROM generate_series(1,200) n;
INSERT INTO osm_canonical.path_segments_g43 SELECT 'fixture:b',43,n,n,2,n,md5('b-node:'||(n/3)::text)::uuid,md5('b-node:'||((n+1)/3)::text)::uuid FROM generate_series(1,200) n;
-- Model the earlier pipeline analyses that did not include provenance.
ANALYZE osm_canonical.ways_g42(way_id,version);
ANALYZE osm_canonical.localities_g42(relation_id,relation_version);
ANALYZE osm_canonical.path_segments_g42(segment_id,source_way_id,source_way_version,logical_path_id);
ALTER TABLE osm_canonical.ways ATTACH PARTITION osm_canonical.ways_g42 FOR VALUES IN ('fixture:a');
ALTER TABLE osm_canonical.ways ATTACH PARTITION osm_canonical.ways_g43 FOR VALUES IN ('fixture:b');
ALTER TABLE osm_canonical.localities ATTACH PARTITION osm_canonical.localities_g42 FOR VALUES IN ('fixture:a');
ALTER TABLE osm_canonical.localities ATTACH PARTITION osm_canonical.localities_g43 FOR VALUES IN ('fixture:b');
ALTER TABLE osm_canonical.path_segments ATTACH PARTITION osm_canonical.path_segments_g42 FOR VALUES IN ('fixture:a');
ALTER TABLE osm_canonical.path_segments ATTACH PARTITION osm_canonical.path_segments_g43 FOR VALUES IN ('fixture:b');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "CREATE ROLE "+identifier+" LOGIN NOSUPERUSER; GRANT USAGE ON SCHEMA osm_catalog,osm_canonical TO "+identifier); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database, config.ConnConfig.User = name, name
	config.ConnConfig.RuntimeParams["application_name"] = name
	config.ConnConfig.RuntimeParams["statement_timeout"] = "7777ms"
	config.ConnConfig.RuntimeParams["lock_timeout"] = "777ms"
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := PostgreSQLUpdateStore{Pool: pool}
	if err := store.RefreshStatistics(ctx); err == nil || !strings.Contains(err.Error(), "maintenance privilege required") {
		t.Fatalf("ANALYZE must not silently skip unprivileged relations: %v", err)
	}
	if _, err := db.Exec(ctx, "GRANT MAINTAIN ON osm_canonical.ways,osm_canonical.localities,osm_canonical.path_segments,osm_catalog.generations TO "+identifier); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshStatistics(ctx); err == nil || !strings.Contains(err.Error(), "ways_g42") {
		t.Fatalf("missing leaf privileges must not be skipped: %v", err)
	}
	if _, err := db.Exec(ctx, "GRANT MAINTAIN ON ALL TABLES IN SCHEMA osm_catalog,osm_canonical TO "+identifier); err != nil {
		t.Fatal(err)
	}
	assertStatistics := func(want int) {
		t.Helper()
		var count int
		// Each root has an inherited row per column; each leaf has a noninherited
		// row. The catalog heap adds its three noninherited column statistics.
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stats
WHERE (schemaname='osm_canonical' AND (inherited OR tablename ~ '_g[0-9]+$'))
   OR (schemaname='osm_catalog' AND tablename='generations')`).Scan(&count); err != nil || count != want {
			t.Fatalf("statistics rows=%d, want %d: %v", count, want, err)
		}
	}
	assertStatistics(8)
	if err := store.RefreshStatistics(ctx); err != nil {
		t.Fatal(err)
	}
	assertStatistics(51) // (4 ways + 4 localities + 8 segments) * (root + 2 leaves) + 3 catalog
	// Partial provenance-only analysis leaves graph selectivity at PostgreSQL's
	// defaults even when generation selectivity is accurate. Cover both together.
	assertGraphStatistics := func() {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stats
WHERE schemaname='osm_canonical' AND tablename LIKE 'path_segments%'
  AND attname IN ('start_graph_node_id','end_graph_node_id')
  AND (inherited OR tablename ~ '_g[0-9]+$')
  AND null_frac=0 AND n_distinct<0`).Scan(&count); err != nil || count != 6 {
			t.Fatalf("graph statistics rows=%d, want root and both leaves for both endpoints: %v", count, err)
		}
	}
	assertGraphStatistics()
	assertSettings := func() {
		t.Helper()
		var statementTimeout, lockTimeout string
		if err := pool.QueryRow(ctx, `SELECT current_setting('statement_timeout'),current_setting('lock_timeout')`).Scan(&statementTimeout, &lockTimeout); err != nil {
			t.Fatal(err)
		}
		if statementTimeout != "7777ms" || lockTimeout != "777ms" {
			t.Fatalf("maintenance settings leaked: statement=%s lock=%s", statementTimeout, lockTimeout)
		}
	}
	assertSettings()
	for _, cancelRefresh := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancel=%t", cancelRefresh), func(t *testing.T) {
			blocker, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `LOCK TABLE ONLY osm_canonical.path_segments IN SHARE UPDATE EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			refreshCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- store.RefreshStatistics(refreshCtx) }()
			// Use a separate connection: db is held by the deliberately conflicting lock.
			observer, err := pgx.ConnectConfig(ctx, adminConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close(context.Background())
			waiting := false
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				if err := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, name).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("refresh did not reach the conflicting maintenance lock")
			}
			readerCtx, readerCancel := context.WithTimeout(ctx, time.Second)
			defer readerCancel()
			var count int
			if err := observer.QueryRow(readerCtx, `SELECT count(*) FROM osm_canonical.ways`).Scan(&count); err != nil || count != 200 {
				t.Fatalf("refresh blocked ordinary readers: count=%d err=%v", count, err)
			}
			if cancelRefresh {
				cancel()
			}
			err = <-done
			if cancelRefresh {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
			} else {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
					t.Fatalf("expected bounded lock timeout, got %v", err)
				}
			}
			if err := blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			assertSettings()
			// No changed source or generation: the same active leaves must be repairable.
			if err := store.RefreshStatistics(ctx); err != nil {
				t.Fatal(err)
			}
			assertStatistics(51)
			assertGraphStatistics()
		})
	}
	var active int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM osm_catalog.generations WHERE state='active'`).Scan(&active); err != nil || active != 2 {
		t.Fatalf("maintenance changed active generations: active=%d err=%v", active, err)
	}
}
