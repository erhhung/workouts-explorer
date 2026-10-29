package osm

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This suite is explicitly opt-in. It applies the real migrations in uniquely
// named schemas on a disposable PostgreSQL/PostGIS 17+ database, never a provider.
type routeCatalogSQLFixture struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	replace *strings.Replacer
}

func newRouteCatalogSQLFixture(t *testing.T) *routeCatalogSQLFixture {
	t.Helper()
	url := os.Getenv("OSM_CATALOG_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OSM_CATALOG_TEST_DATABASE_URL is not configured (disposable PostGIS database only)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := fmt.Sprintf("route_catalog_test_%d", time.Now().UnixNano())
	f := &routeCatalogSQLFixture{ctx, pool, strings.NewReplacer("osm_catalog", name, "osm_active", name+"_active")}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE; DROP SCHEMA IF EXISTS "+pgx.Identifier{name + "_active"}.Sanitize()+" CASCADE")
		if err != nil {
			t.Error(err)
		}
	})
	for _, file := range []string{"001_foundation.sql", "006_promotion_events.sql", "010_route_region_catalog_state.sql"} {
		if strings.HasPrefix(file, "010_") {
			// Exercise copying an existing reader ACL without creating roles or
			// depending on installation-specific role names.
			f.exec(t, `GRANT SELECT ON osm_catalog.regions TO pg_read_all_stats`)
		}
		sql, err := migrations.Files.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(t, strings.Split(string(sql), "-- +goose Down")[0])
	}
	return f
}

func (f *routeCatalogSQLFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, f.replace.Replace(sql), args...); err != nil {
		t.Fatal(err)
	}
}

func (f *routeCatalogSQLFixture) row(sql string, args ...any) pgx.Row {
	return f.pool.QueryRow(f.ctx, f.replace.Replace(sql), args...)
}

type routeCatalogSchemaTx struct {
	pgx.Tx
	replace *strings.Replacer
}

func (tx routeCatalogSchemaTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return tx.Tx.QueryRow(ctx, tx.replace.Replace(sql), args...)
}

func (tx routeCatalogSchemaTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return tx.Tx.Query(ctx, tx.replace.Replace(sql), args...)
}

func (f *routeCatalogSQLFixture) catalog(t *testing.T) RouteRegionCatalog {
	t.Helper()
	tx, err := f.pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	catalog, err := readRouteRegionCatalog(f.ctx, routeCatalogSchemaTx{tx, f.replace}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func (f *routeCatalogSQLFixture) region(t *testing.T, id, box string, configured bool) {
	t.Helper()
	f.exec(t, `INSERT INTO osm_catalog.regions
		(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary,configured)
		VALUES ('fixture:'||$1,'fixture',$1,$1,'https://example.test/index','https://example.test/source',
		ST_Multi(ST_GeomFromText($2,4326)),$3)`, id, box, configured)
}

func (f *routeCatalogSQLFixture) generation(t *testing.T, region string, active bool) int64 {
	t.Helper()
	var generation int64
	// Identity-generated IDs determine the schema name only in production. The
	// fixture names need only satisfy the existing catalog constraint and UNIQUE.
	err := f.row(`INSERT INTO osm_catalog.generations
		(region_id,state,schema_name,source_url,importer_version,derivation_version,promoted_at)
		VALUES ($1,CASE WHEN $2 THEN 'active' ELSE 'building' END,$3,'https://example.test/source',1,1,
		CASE WHEN $2 THEN transaction_timestamp() END) RETURNING id`, "fixture:"+region, active,
		fmt.Sprintf("osm_build_%d", time.Now().UnixNano())).Scan(&generation)
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

const catalogBigBox = "POLYGON((0 0,10 0,10 10,0 10,0 0))"
const catalogSmallBox = "POLYGON((1 1,3 1,3 3,1 3,1 1))"

// Keep the pre-010 encoding independent of the materialized proof expression.
const legacyRouteCatalogFingerprintSQL = `SELECT pg_catalog.sha256(
	pg_catalog.convert_to('route-region-catalog-v1','UTF8') || COALESCE(string_agg(
		pg_catalog.sha256(pg_catalog.int4send(octet_length(pg_catalog.convert_to(region.id,'UTF8'))) ||
			pg_catalog.convert_to(region.id,'UTF8') || pg_catalog.boolsend(region.configured) ||
			pg_catalog.sha256(ST_AsEWKB(region.boundary,'NDR')) ||
			pg_catalog.int8send(COALESCE(generation.id,0))),
		''::bytea ORDER BY region.id COLLATE "C"),''::bytea))
	FROM osm_catalog.regions region LEFT JOIN osm_catalog.generations generation
	ON generation.region_id=region.id AND generation.state='active'`

func TestRouteCatalogFingerprintsIntegration(t *testing.T) {
	f := newRouteCatalogSQLFixture(t)
	f.region(t, "large", catalogBigBox, true)
	f.generation(t, "large", true)
	check := func(t *testing.T, before RouteRegionCatalog, fullChanged, selectionChanged, revisionChanged bool) RouteRegionCatalog {
		t.Helper()
		after := f.catalog(t)
		if (after.Fingerprint != before.Fingerprint) != fullChanged ||
			(after.SelectionFingerprint != before.SelectionFingerprint) != selectionChanged ||
			(after.Revision > before.Revision) != revisionChanged || after.Revision < before.Revision {
			t.Fatalf("unexpected proof change: before=%+v after=%+v", before, after)
		}
		var legacy []byte
		if err := f.row(legacyRouteCatalogFingerprintSQL).Scan(&legacy); err != nil {
			t.Fatal(err)
		}
		if after.Fingerprint != hex.EncodeToString(legacy) {
			t.Fatal("legacy cohort full fingerprint changed encoding")
		}
		return after
	}
	before := f.catalog(t)
	for _, test := range []struct {
		name, sql                 string
		full, selection, revision bool
	}{
		{"equal region write", `UPDATE osm_catalog.regions SET configured=configured,boundary=boundary,display_name=display_name`, false, false, false},
		{"unrelated region write", `UPDATE osm_catalog.regions SET updated_at=clock_timestamp(),advertised_download_bytes=123`, false, false, false},
		{"unrelated generation write", `UPDATE osm_catalog.generations SET downloaded_bytes=123,validation='{"progress":1}'`, false, false, false},
		{"equal generation write", `UPDATE osm_catalog.generations SET state=state,region_id=region_id`, false, false, false},
		{"name only", `UPDATE osm_catalog.regions SET display_name='Renamed'`, false, false, true},
		{"shape", `UPDATE osm_catalog.regions SET boundary=ST_Multi(ST_GeomFromText('POLYGON((0 0,9 0,9 9,0 9,0 0))',4326))`, true, true, true},
		{"configuration", `UPDATE osm_catalog.regions SET configured=false`, true, true, true},
		{"reconfigure", `UPDATE osm_catalog.regions SET configured=true`, true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var oldXmin, newXmin string
			if err := f.row(`SELECT xmin::text FROM osm_catalog.route_region_catalog_state`).Scan(&oldXmin); err != nil {
				t.Fatal(err)
			}
			f.exec(t, test.sql)
			before = check(t, before, test.full, test.selection, test.revision)
			if err := f.row(`SELECT xmin::text FROM osm_catalog.route_region_catalog_state`).Scan(&newXmin); err != nil {
				t.Fatal(err)
			}
			if (oldXmin != newXmin) != test.revision {
				t.Fatal("equal refresh performed catalog DML, or real change did not")
			}
		})
	}
	f.region(t, "small", catalogSmallBox, true)
	before = check(t, before, true, true, true) // New competing boundaries, even inactive.
	f.generation(t, "small", true)
	before = check(t, before, true, true, true) // A smaller configured active competitor.
	var replacement int64
	f.exec(t, `UPDATE osm_catalog.generations SET state='retired',retired_at=now() WHERE region_id='fixture:small'`)
	before = check(t, before, true, true, true)
	replacement = f.generation(t, "small", true)
	before = check(t, before, true, true, true)
	// Retire+activate in one transaction: selection membership is unchanged.
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err := tx.Exec(f.ctx, f.replace.Replace(`UPDATE osm_catalog.generations SET state='retired',retired_at=now()
		WHERE region_id='fixture:small' AND state='active';
		INSERT INTO osm_catalog.generations(region_id,state,schema_name,source_url,importer_version,derivation_version,promoted_at)
		VALUES ('fixture:small','active','osm_build_999','https://example.test/source',1,1,now())`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	before = check(t, before, true, false, true)
	if before.ActiveGenerations["fixture:small"] == replacement {
		t.Fatal("replacement head not published")
	}
	f.region(t, "unconfigured", catalogSmallBox, false)
	before = check(t, before, true, true, true)
	f.generation(t, "unconfigured", true)
	before = check(t, before, true, false, true)
	if before.ActiveGenerations["fixture:unconfigured"] == 0 || len(before.ConfiguredRegions) != 2 {
		t.Fatal("all-active and configured-name catalogs were conflated")
	}
	f.exec(t, `DELETE FROM osm_catalog.regions WHERE id='fixture:unconfigured' AND false`)
	before = check(t, before, false, false, false)
	f.region(t, "removed", catalogSmallBox, false)
	before = check(t, before, true, true, true)
	f.exec(t, `DELETE FROM osm_catalog.regions WHERE id='fixture:removed'`)
	check(t, before, true, true, true)
}

func TestRouteCatalogRollbackAndConcurrentRevisionIntegration(t *testing.T) {
	f := newRouteCatalogSQLFixture(t)
	f.region(t, "large", catalogBigBox, true)
	before := f.catalog(t)
	first, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(f.ctx)
	if _, err := first.Exec(f.ctx, f.replace.Replace(`UPDATE osm_catalog.regions SET display_name='rollback'`)); err != nil {
		t.Fatal(err)
	}
	if got := f.catalog(t); !reflect.DeepEqual(got, before) {
		t.Fatal("uncommitted revision was visible")
	}
	if err := first.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.catalog(t); !reflect.DeepEqual(got, before) {
		t.Fatal("rollback advanced published head")
	}

	stale, err := f.pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Rollback(f.ctx)
	if _, err := stale.Exec(f.ctx, f.replace.Replace(`SELECT revision FROM osm_catalog.route_region_catalog_state`)); err != nil {
		t.Fatal(err)
	}
	first, err = f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(f.ctx)
	if _, err := first.Exec(f.ctx, f.replace.Replace(`UPDATE osm_catalog.regions SET display_name='first'`)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.pool.Exec(f.ctx, f.replace.Replace(`UPDATE osm_catalog.regions SET display_name='second'`))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("second writer did not wait for singleton commit: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := first.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after := f.catalog(t)
	if after.Revision != before.Revision+2 || after.ConfiguredRegions[0].DisplayName != "second" {
		t.Fatalf("commit-ordered revision=%+v before=%+v", after, before)
	}
	_, err = stale.Exec(f.ctx, f.replace.Replace(`UPDATE osm_catalog.regions SET display_name='stale'`))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
		t.Fatalf("stale RR writer must fail, not publish: %v", err)
	}
	if got := f.catalog(t); !reflect.DeepEqual(got, after) {
		t.Fatal("stale snapshot published catalog state")
	}
}

func TestRouteCatalogStoredAreaROIParityIntegration(t *testing.T) {
	f := newRouteCatalogSQLFixture(t)
	f.region(t, "large", catalogBigBox, true)
	f.region(t, "small", catalogSmallBox, true)
	f.region(t, "tie", catalogSmallBox, true)
	f.region(t, "inactive", "POLYGON((20 20,21 20,21 21,20 21,20 20))", true)
	f.generation(t, "large", true)
	f.generation(t, "small", true)
	f.generation(t, "tie", true)
	oldSQL := strings.ReplaceAll(resolveRouteRegionsSQL, "region.geographic_area_m2", "ST_Area(region.boundary::geography)")
	for _, point := range []RouteCoordinate{{2, 2}, {1, 1}, {0, 0}, {5, 5}, {20.5, 20.5}, {30, 30}} {
		tx, err := f.pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		wrapped := routeCatalogSchemaTx{tx, f.replace}
		got, err := resolveRouteRegions(f.ctx, wrapped, []RouteCoordinate{point})
		if err != nil {
			t.Fatal(err)
		}
		var ordinal int
		var observation pointRegionObservation
		err = wrapped.QueryRow(f.ctx, oldSQL, []float64{point.Longitude}, []float64{point.Latitude}).Scan(
			&ordinal, &observation.regionID, &observation.generation, &observation.inCatalog)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, readinessFromObservations([]pointRegionObservation{observation})) {
			t.Fatalf("stored-area ROI differs at fixture point: %+v", got)
		}
		if point.Longitude == 2 && (len(got.Regions) != 1 || got.Regions[0].RegionID != "fixture:small") {
			t.Fatal("smallest area / id tiebreak changed")
		}
		tx.Rollback(f.ctx)
	}
	var valid bool
	if err := f.row(`SELECT bool_and(boundary_sha256=sha256(ST_AsEWKB(boundary,'NDR')) AND
		geographic_area_m2=ST_Area(boundary::geography)) FROM osm_catalog.regions`).Scan(&valid); err != nil || !valid {
		t.Fatalf("stored boundary metadata invalid: %v", err)
	}
	for _, table := range []string{"regions", "generations", "route_region_catalog_state"} {
		if _, err := f.pool.Exec(f.ctx, f.replace.Replace("TRUNCATE osm_catalog."+table+" CASCADE")); err == nil {
			t.Fatalf("TRUNCATE %s bypassed metadata", table)
		}
	}
}

func TestRouteCatalogMigrationACLAndBulkIntegration(t *testing.T) {
	f := newRouteCatalogSQLFixture(t)
	var valid bool
	if err := f.row(`SELECT schema_version=10 AND minimum_runtime_version=10 FROM osm_catalog.schema_metadata WHERE singleton`).Scan(&valid); err != nil || !valid {
		t.Fatalf("OSM schema 010 watermark invalid: %v", err)
	}
	if err := f.row(`SELECT state.relowner=regions.relowner
		AND has_table_privilege('pg_read_all_stats',state.oid,'SELECT')
		AND NOT has_table_privilege('pg_read_all_stats',state.oid,'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER,REFERENCES')
		AND NOT EXISTS (SELECT 1 FROM aclexplode(state.relacl) WHERE grantee=0)
		AND NOT EXISTS (SELECT 1 FROM pg_proc helper WHERE helper.pronamespace=state.relnamespace
		    AND helper.proname IN ('lock_route_region_catalog','refresh_route_region_catalog','deny_route_catalog_truncate')
		    AND (helper.proowner<>regions.relowner OR EXISTS
		        (SELECT 1 FROM aclexplode(helper.proacl) WHERE grantee=0 AND privilege_type='EXECUTE')))
		FROM pg_class state,pg_class regions
		WHERE state.oid='osm_catalog.route_region_catalog_state'::regclass AND regions.oid='osm_catalog.regions'::regclass`).Scan(&valid); err != nil || !valid {
		t.Fatalf("catalog owner/reader/private-helper ACL invalid: %v", err)
	}
	before := f.catalog(t)
	f.exec(t, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary,configured)
		SELECT 'fixture:bulk-'||n,'fixture','bulk-'||n,'Bulk '||n,'https://example.test/index','https://example.test/source',
		ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 1,0 0))',4326)),false
		FROM generate_series(1,512) n`)
	after := f.catalog(t)
	if after.Revision != before.Revision+1 {
		t.Fatalf("512-row statement refreshed per row: before=%d after=%d", before.Revision, after.Revision)
	}
	f.exec(t, `UPDATE osm_catalog.regions SET boundary=boundary,configured=configured,display_name=display_name`)
	if got := f.catalog(t); !reflect.DeepEqual(got, after) {
		t.Fatal("equal bulk update changed published state")
	}
}
