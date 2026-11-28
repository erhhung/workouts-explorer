package osm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm/migrations"
	"github.com/jackc/pgx/v5"
)

func TestResidentialContextClassifiesHomesNotDrivewayNamesPostgreSQL(t *testing.T) {
	if os.Getenv("RESIDENTIAL_CONTEXT_TEST_DISPOSABLE") != "1" {
		t.Skip("owned disposable PostGIS fixture required")
	}
	var previous, optimized map[int]map[string]string
	t.Run("previous_plan", func(t *testing.T) { previous = residentialContextFixture(t, false) })
	t.Run("road_parameterized_plan", func(t *testing.T) { optimized = residentialContextFixture(t, true) })
	if !reflect.DeepEqual(previous, optimized) {
		t.Fatal("query planning correction changed public residential evidence or classification")
	}
}

func residentialContextFixture(t *testing.T, optimized bool) map[int]map[string]string {
	t.Helper()
	f := newRouteCatalogSQLFixture(t)
	migration, err := migrations.Files.ReadFile("008_resumable_generation_stages.sql")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, strings.Split(string(migration), "-- +goose Down")[0])
	f.region(t, "homes", catalogBigBox)
	generation := f.generation(t, "homes", false)
	schema := fmt.Sprintf("osm_build_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	f.exec(t, "CREATE SCHEMA "+quoted)
	t.Cleanup(func() { _, _ = f.pool.Exec(f.ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	f.exec(t, `UPDATE osm_catalog.generations SET importer_version=4,derivation_version=21,schema_name=$2 WHERE id=$1`, generation, schema)
	f.exec(t, `INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state)
        VALUES($1,'residential-context',4,repeat('0',64),'running')`, generation)
	f.exec(t, `SET search_path TO `+quoted+`,public;
        CREATE TABLE ways(way_id bigint,version integer DEFAULT 1,tags jsonb,node_ids jsonb,geom geometry(LineString,4326));
        CREATE INDEX ways_geography_gist ON ways USING gist((geom::geography));
        CREATE TABLE residential_building_ways(way_id bigint,version integer DEFAULT 1,tags jsonb,geom geometry(Polygon,4326));
        CREATE TABLE residential_building_relations(relation_id bigint,version integer,tags jsonb,members jsonb,geom geometry(MultiPolygon,4326));
        CREATE TABLE residential_landuse_ways(way_id bigint,geom geometry(Polygon,4326));
        CREATE TABLE residential_landuse_relations(relation_id bigint,geom geometry(MultiPolygon,4326));
        CREATE TABLE residential_address_nodes(node_id bigint,tags jsonb,geom geometry(Point,4326));
        INSERT INTO residential_landuse_ways VALUES(1,ST_MakeEnvelope(-.01,-.01,.05,.05,4326));`)
	degree := 1.0 / 111320.0
	addRoad := func(id int, y, length float64, tags string) {
		t.Helper()
		f.exec(t, `INSERT INTO `+quoted+`.ways(way_id,tags,node_ids,geom) VALUES($1,$2::jsonb,$3::jsonb,
            ST_MakeLine(ST_SetSRID(ST_MakePoint(0,$4),4326),ST_SetSRID(ST_MakePoint($5,$4),4326)))`,
			id, tags, fmt.Sprintf("[%d,%d]", id*10, id*10+1), y*degree, length*degree)
		f.exec(t, `INSERT INTO `+quoted+`.ways(way_id,tags,node_ids,geom) VALUES($1,'{"highway":"residential"}',$2::jsonb,
            ST_MakeLine(ARRAY[ST_SetSRID(ST_MakePoint(-.0002,$3),4326),ST_SetSRID(ST_MakePoint(0,$3),4326),
                ST_SetSRID(ST_MakePoint(0,$3+.0002),4326)]))`, id+10000, fmt.Sprintf("[%d,%d,%d]", id*10+2, id*10, id*10+3), y*degree)
	}
	buildingID := 100000
	addBuilding := func(x, y float64, tags string) {
		t.Helper()
		buildingID++
		f.exec(t, `INSERT INTO `+quoted+`.residential_building_ways(way_id,tags,geom)
            VALUES($1,$2::jsonb,ST_MakeEnvelope($3::double precision,$4::double precision,$3::double precision+$5::double precision,$4::double precision+$5::double precision,4326))`, buildingID, tags, (x-4)*degree, (y-4)*degree, 8*degree)
	}
	addRoad(100, 0, 100, `{"highway":"service","service":"driveway"}`)
	for _, x := range []float64{25, 50, 75} {
		addBuilding(x, 16, `{"building":"house"}`)
	}
	addRoad(200, 200, 100, `{"highway":"service","service":"driveway","name":"Named Single House Drive","access":"private"}`)
	addBuilding(90, 216, `{"building":"house"}`)
	addBuilding(60, 216, `{"building":"garage"}`)
	addBuilding(30, 216, `{"building":"shed"}`)
	addRoad(300, 400, 100, `{"highway":"service","service":"driveway"}`)
	addBuilding(85, 416, `{"building":"apartments","building:flats":"12"}`)
	addRoad(400, 600, 100, `{"highway":"service","service":"driveway"}`)
	for _, x := range []float64{25, 50, 75} {
		addBuilding(x, 616, `{"building":"yes","addr:street":"Fixture","addr:housenumber":"1"}`)
	}
	addRoad(500, 800, 100, `{"highway":"service","service":"driveway"}`)
	for _, x := range []float64{25, 50, 75} {
		addBuilding(x, 816, `{"building":"yes"}`)
	}
	addRoad(600, 1000, 100, `{"highway":"service","service":"shared_driveway"}`)
	addRoad(700, 1200, 100, `{"highway":"service","service":"driveway"}`)
	addBuilding(80, 1216, `{"building":"yes"}`)
	for i, unit := range []string{"A", "B", "C"} {
		f.exec(t, `INSERT INTO `+quoted+`.residential_address_nodes VALUES($1,$2::jsonb,ST_SetSRID(ST_MakePoint($3,$4),4326))`,
			7000+i, fmt.Sprintf(`{"addr:street":"Fixture","addr:housenumber":"2","addr:unit":%q}`, unit), (78+float64(i))*degree, 1216*degree)
	}
	// A one-house leaf connected to the shared street must not inherit its class.
	f.exec(t, `UPDATE `+quoted+`.ways SET node_ids='[1000,1002,1001]',geom=ST_MakeLine(ARRAY[
        ST_SetSRID(ST_MakePoint(0,0),4326),ST_SetSRID(ST_MakePoint($1,0),4326),ST_SetSRID(ST_MakePoint($2,0),4326)]) WHERE way_id=100`, 50*degree, 100*degree)
	f.exec(t, `INSERT INTO `+quoted+`.ways(way_id,tags,node_ids,geom) VALUES(800,'{"highway":"service","service":"driveway"}',
        '[1002,8001]',ST_MakeLine(ST_SetSRID(ST_MakePoint($1,0),4326),ST_SetSRID(ST_MakePoint($1,$2),4326)))`, 50*degree, -60*degree)
	addBuilding(50, -70, `{"building":"house"}`)
	f.exec(t, `INSERT INTO `+quoted+`.residential_building_ways(way_id,tags,geom)
        SELECT 200000+i,'{"building":"garage"}',ST_MakeEnvelope(1+i*.0001,1,1+i*.0001+.00005,1.00005,4326)
        FROM generate_series(1,20000) i`)

	source, err := os.ReadFile(filepath.Join("..", "..", "osm", "batches", "residential-context-batched.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.NewReplacer(`:"OSM_BUILD_SCHEMA"`, quoted, `:'OSM_GENERATION_ID'`, fmt.Sprintf("'%d'", generation)).Replace(string(source))
	if !optimized {
		sql = strings.Replace(sql, "                OFFSET 0\n", "", 1)
	}
	for batch := 0; batch < 100; batch++ {
		var phase string
		if err := f.row(`SELECT COALESCE(cursor->>'phase','init') FROM osm_catalog.generation_stages WHERE generation_id=$1 AND stage='residential-context'`, generation).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		if optimized && phase == "frontage" {
			assertResidentialFrontageSpatialPlan(t, f, quoted, sql)
		}
		f.exec(t, sql)
		var done bool
		if err := f.row(`SELECT state='completed' FROM osm_catalog.generation_stages WHERE generation_id=$1 AND stage='residential-context'`, generation).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		if batch == 99 {
			t.Fatal("context stage did not finish within its fixture bound")
		}
	}
	result := map[int]map[string]string{}
	for id, want := range map[int]string{100: "shared", 200: "single_house", 300: "shared", 400: "single_house", 500: "shared", 600: "shared", 700: "shared", 800: "single_house"} {
		var tags []byte
		if err := f.pool.QueryRow(f.ctx, "SELECT tags FROM "+quoted+".ways WHERE way_id=$1", id).Scan(&tags); err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		if err := json.Unmarshal(tags, &got); err != nil {
			t.Fatal(err)
		}
		if got["workouts:residential_access"] != want || got["workouts:residential_context_version"] != "1" {
			t.Errorf("way %d classification=%v want=%s", id, got, want)
		}
		result[id] = got
	}
	var rawRemoved bool
	if err := f.pool.QueryRow(f.ctx, `SELECT to_regclass($1) IS NULL`, schema+".residential_building_ways").Scan(&rawRemoved); err != nil || !rawRemoved {
		t.Fatalf("source context scratch was retained: %t %v", rawRemoved, err)
	}
	return result
}

func assertResidentialFrontageSpatialPlan(t *testing.T, f *routeCatalogSQLFixture, quoted, source string) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err = tx.Exec(f.ctx, `SET LOCAL search_path TO `+quoted+`,public;
        CREATE TEMP TABLE residential_batch ON COMMIT DROP AS SELECT way_id FROM residential_access_roads ORDER BY way_id LIMIT 500`); err != nil {
		t.Fatal(err)
	}
	query := "WITH evidence AS MATERIALIZED (" + strings.Split(strings.Split(source, "        WITH evidence AS MATERIALIZED (")[1], "        SELECT max(way_id) INTO next_way")[0]
	var raw []byte
	if err = tx.QueryRow(f.ctx, "EXPLAIN (FORMAT JSON) "+query).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []map[string]any
	if err = json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	indexed := false
	var walk func(map[string]any)
	walk = func(node map[string]any) {
		if node["Relation Name"] == "residential_building_ways" {
			condition, _ := node["Index Cond"].(string)
			if !strings.Contains(condition, "road.geom") {
				t.Errorf("frontage footprint lookup is not spatially bounded by its batch road: %s", raw)
			}
			indexed = true
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
	}
	walk(plans[0]["Plan"].(map[string]any))
	if !indexed {
		t.Fatal("frontage plan did not include its footprint lookup")
	}
}
