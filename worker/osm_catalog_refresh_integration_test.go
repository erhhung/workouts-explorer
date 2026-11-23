package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogRefreshPostgreSQL(t *testing.T) {
	workerURL, catalogURL, ownerURL, osmOwnerURL := os.Getenv("WORKER_DATABASE_URL"), os.Getenv("OSM_CATALOG_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_CATALOG_TEST_OWNER_URL")
	if workerURL == "" || catalogURL == "" || ownerURL == "" || osmOwnerURL == "" {
		t.Skip("isolated catalog integration databases required")
	}
	if !strings.Contains(ownerURL, "workouts_admin_test_") || !strings.Contains(osmOwnerURL, "osm_catalog_test") {
		t.Fatal("refusing non-test databases")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := func(url string) *pgxpool.Pool {
		p, e := pgxpool.New(ctx, url)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(p.Close)
		return p
	}
	db, catalog, owner, osmOwner := pool(workerURL), pool(catalogURL), pool(ownerURL), pool(osmOwnerURL)
	exec := func(pool *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var admin uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal("run administrator foundation fixture first:", err)
	}
	// The foundation test leaves one real API-admitted queued command.
	runner := NewCatalogRefreshRunner(db, catalog, []string{"geofabrik", "unsupported"}, 5, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var active, maxActive, heads atomic.Int32
	var second atomic.Bool
	index := map[string]any{"type": "FeatureCollection"}
	features := []any{}
	for i := range 12 {
		id := fmt.Sprintf("us/native-%02d", i)
		if i == 0 {
			id = "us/california/norcal"
		}
		features = append(features, map[string]any{"properties": map[string]any{"id": id, "name": fmt.Sprintf("Native region %02d", i), "parent": "us", "urls": map[string]string{"pbf": fmt.Sprintf("https://download.geofabrik.de/native-%02d-latest.osm.pbf", i)}}, "geometry": json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`)})
	}
	index["features"] = features
	encoded, _ := json.Marshal(index)
	runner.HTTPClient = &http.Client{Transport: catalogTransport(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}
		if request.URL.String() == osm.GeofabrikIndexURL {
			if request.Method != "GET" {
				t.Error("index not GET")
			}
			response.Body = io.NopCloser(strings.NewReader(string(encoded)))
			return response, nil
		}
		if request.Method != "HEAD" {
			t.Error("extract contents requested")
			return response, nil
		}
		heads.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if old >= current || maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(25 * time.Millisecond):
		}
		if strings.Contains(request.URL.Path, "native-08") || second.Load() && strings.Contains(request.URL.Path, "native-01") {
			response.StatusCode = 503
			return response, nil
		}
		if strings.Contains(request.URL.Path, "latest") {
			response.StatusCode = 307
			response.Header.Set("Location", strings.Replace(request.URL.String(), "latest", "261117", 1))
			return response, nil
		}
		response.Header.Set("Content-Length", "123456")
		response.Header.Set("Last-Modified", time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC).Format(http.TimeFormat))
		return response, nil
	})}
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("refresh: %t %v", worked, err)
	}
	var valid bool
	if err := owner.QueryRow(ctx, `SELECT state='partially_succeeded' AND progress->>'total'='12' AND progress->>'completed'='12' AND progress->>'failed'='1' FROM app.osm_catalog_refresh_jobs ORDER BY created_at DESC LIMIT 1`).Scan(&valid); err != nil || !valid {
		t.Fatalf("progress not durable: %v", err)
	}
	if maxActive.Load() != 5 {
		t.Fatalf("metadata ceiling=%d want 5", maxActive.Load())
	}
	if err := osmOwner.QueryRow(ctx, `SELECT count(*)=12 AND count(*) FILTER(WHERE id='geofabrik:norcal')=1 AND count(*) FILTER(WHERE id='geofabrik:us/native-01')=1 FROM osm_catalog.regions`).Scan(&valid); err != nil || !valid {
		t.Fatalf("hierarchical/legacy identity: %v", err)
	}
	if _, err := catalog.Exec(ctx, `UPDATE osm_catalog.generations SET downloaded_bytes=1`); err == nil {
		t.Fatal("catalog role can update generations")
	}
	if _, err := catalog.Exec(ctx, `DELETE FROM osm_catalog.regions`); err == nil {
		t.Fatal("catalog role can delete regions")
	}
	exec(osmOwner, `CREATE TABLE osm_canonical.ways_native(id int PRIMARY KEY,payload text);CREATE TABLE osm_canonical.localities_native(id int);CREATE TABLE osm_canonical.segments_native(id int);
        INSERT INTO osm_canonical.ways_native SELECT i,repeat(md5(i::text),100) FROM generate_series(1,100) i;`)
	var generation int64
	if err := osmOwner.QueryRow(ctx, `INSERT INTO osm_catalog.generations(region_id,state,schema_name,source_url,importer_version,derivation_version)
        VALUES('geofabrik:norcal','building','osm_build_9001','https://download.geofabrik.de/native-00-latest.osm.pbf',1,1) RETURNING id`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	exec(osmOwner, `UPDATE osm_catalog.generations SET downloaded_bytes=999,source_sha256=repeat('a',64),source_header_timestamp='2026-11-16Z' WHERE id=$1`, generation)
	exec(osmOwner, `UPDATE osm_catalog.generations SET state='active',promoted_at=clock_timestamp() WHERE id=$1`, generation)
	exec(osmOwner, `INSERT INTO osm_catalog.region_storage(region_id,generation_id,ways_relation,localities_relation,path_segments_relation,build_schema)
        VALUES('geofabrik:norcal',$1,'ways_native','localities_native','segments_native','osm_build_9001')`, generation)
	if err := osmOwner.QueryRow(ctx, `SELECT downloaded_at IS NOT NULL AND processed_database_bytes=osm_catalog.region_database_bytes(region_id) AND database_measured_at IS NOT NULL
        FROM osm_catalog.generations WHERE id=$1`, generation).Scan(&valid); err != nil || !valid {
		t.Fatalf("download/storage snapshot missing: %v", err)
	}
	second.Store(true)
	next, lease := uuid.New(), uuid.New()
	exec(owner, `INSERT INTO app.osm_catalog_refresh_jobs(id,administrator_id,providers,state,attempt,lease_token,lease_expires_at)
        VALUES($1,$2,ARRAY['geofabrik'],'running',1,$3,clock_timestamp()-interval '1 second')`, next, admin, lease)
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("reclaimed refresh: %t %v", worked, err)
	}
	if err := owner.QueryRow(ctx, `SELECT attempt=2 AND state='partially_succeeded' AND progress->>'failed'='2' FROM app.osm_catalog_refresh_jobs WHERE id=$1`, next).Scan(&valid); err != nil || !valid {
		t.Fatalf("recovery did not persist: %v", err)
	}
	if err := owner.QueryRow(ctx, `SELECT (payload->>'pbfBytes')::bigint=123456 AND payload->>'metadataErrorCode'='provider-metadata-unavailable' FROM app.osm_region_catalog WHERE id='geofabrik:us/native-01'`).Scan(&valid); err != nil || !valid {
		t.Fatalf("last-known metadata lost: %v", err)
	}
	if err := owner.QueryRow(ctx, `SELECT (payload->>'downloadedBytes')::bigint=999 AND payload->>'downloadedAt' IS NOT NULL
        AND (payload->>'databaseBytes')::bigint>0 AND payload->>'generation'=$1 FROM app.osm_region_catalog WHERE id='geofabrik:norcal'`, fmt.Sprint(generation)).Scan(&valid); err != nil || !valid {
		t.Fatalf("active generation projection invalid: %v", err)
	}
	if worked, err := runner.RunOnce(ctx); worked || err != nil {
		t.Fatalf("idle claim: %t %v", worked, err)
	}
	unsupported := uuid.New()
	exec(owner, `INSERT INTO app.osm_catalog_refresh_jobs(id,administrator_id,providers) VALUES($1,$2,ARRAY['unsupported'])`, unsupported, admin)
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT state='failed' AND failure_code='catalog-refresh-unavailable' FROM app.osm_catalog_refresh_jobs WHERE id=$1`, unsupported).Scan(&valid); err != nil || !valid {
		t.Fatalf("unsupported provider not safe: %v", err)
	}
	t.Logf("HEAD-only metadata requests=%d max concurrency=%d; durable retry, storage and last-known metadata verified", heads.Load(), maxActive.Load())
}
