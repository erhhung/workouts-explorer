package osm

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBannerRegionalGenerationUsesDurableHighWatermarkIntegration(t *testing.T) {
	raw := os.Getenv("OSM_BANNER_NUMBER_TEST_URL")
	if raw == "" || os.Getenv("OSM_BANNER_NUMBER_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable OSM banner fixture required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path != "/coverage_test_osm" || parsed.RawQuery != "sslmode=disable" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		t.Fatal("refusing non-disposable banner fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	part := "banner-" + uuid.NewString()[:8]
	region := "geofabrik:" + part
	if _, err := pool.Exec(ctx, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary,catalog_available,generation_high_watermark)
        VALUES($1,'geofabrik',$2,'Banner fixture','https://download.geofabrik.de/index-v1.json','https://download.geofabrik.de/banner-latest.osm.pbf',ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 0))',4326)),true,17)`, region, part); err != nil {
		t.Fatal(err)
	}
	store := PostgreSQLUpdateStore{Pool: pool}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		rows, err := pool.Query(cleanup, `SELECT id FROM osm_catalog.generations WHERE region_id=$1 AND state IN('building','validating')`, region)
		if err == nil {
			var ids []int64
			for rows.Next() {
				var id int64
				_ = rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
			for _, id := range ids {
				_ = store.Abort(cleanup, id, region, "fixture cleanup")
			}
		}
		_, _ = pool.Exec(cleanup, `DELETE FROM osm_catalog.generations WHERE region_id=$1`, region)
		_, _ = pool.Exec(cleanup, `DELETE FROM osm_catalog.regions WHERE id=$1`, region)
	}()
	versions := ToolVersions{Osmium: "fixture", Osm2pgsql: "fixture"}
	first, err := store.Reserve(ctx, region, versions)
	if err != nil || first.RegionalGeneration != 18 {
		t.Fatalf("first generation label=%d error=%v", first.RegionalGeneration, err)
	}
	if err := store.RecordDownload(ctx, first.ID, DownloadResult{Bytes: 3, SHA256: strings.Repeat("a", 64)}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state,started_at,finished_at)
        SELECT $1,stage,ordinal-1,repeat('a',64),'completed',clock_timestamp(),clock_timestamp()
        FROM unnest(ARRAY['tags-filter','check-refs','osm2pgsql']) WITH ORDINALITY AS input(stage,ordinal)`, first.ID); err != nil {
		t.Fatal(err)
	}
	resumed, _, err := store.Resume(ctx, first.ID, region, versions)
	if err != nil || resumed.ID != first.ID || resumed.RegionalGeneration != 18 {
		t.Fatal("resume changed the job/generation label", resumed, err)
	}
	if err := store.Abort(ctx, first.ID, region, "fixture failed candidate"); err != nil {
		t.Fatal(err)
	}
	second, err := store.Reserve(ctx, region, versions)
	if err != nil || second.ID == first.ID || second.RegionalGeneration != 18 {
		t.Fatal("failed job consumed a regional generation", second, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE osm_catalog.generations SET state='active',promoted_at=clock_timestamp() WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	assertPromotion := func(active bool) {
		t.Helper()
		regions, err := CatalogRegions(ctx, pool, "geofabrik")
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range regions {
			if value.ID != region {
				continue
			}
			if value.PromotionNumbers[strconv.FormatInt(second.ID, 10)] != 18 || len(value.PromotionNumbers) != 1 {
				t.Fatal("catalog lost historical promotion number or numbered a failed job", value.PromotionNumbers)
			}
			if (value.BuildID != nil) != active {
				t.Fatal("unexpected active generation", value.BuildID)
			}
			return
		}
		t.Fatal("catalog region missing")
	}
	assertPromotion(true)
	if _, err := pool.Exec(ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	assertPromotion(false)
}
