package osm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm/migrations"
	"github.com/jackc/pgx/v5"
)

func TestIdentityLabelsSequentialPlanPreservesLabelsAndOverridesPostgreSQL(t *testing.T) {
	if os.Getenv("IDENTITY_LABELS_TEST_DISPOSABLE") != "1" {
		t.Skip("owned disposable PostGIS fixture required")
	}
	var before, after []byte
	t.Run("previous_access_plan", func(t *testing.T) { before = identityLabelsFixture(t, false) })
	t.Run("sequential_access_plan", func(t *testing.T) { after = identityLabelsFixture(t, true) })
	if !bytes.Equal(before, after) {
		t.Fatalf("access plan changed labels or neighbor overrides:\nprevious=%s\nnew=%s", before, after)
	}
}

func identityLabelsFixture(t *testing.T, optimized bool) []byte {
	t.Helper()
	f := newRouteCatalogSQLFixture(t)
	migration, err := migrations.Files.ReadFile("008_resumable_generation_stages.sql")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, strings.Split(string(migration), "-- +goose Down")[0])
	f.region(t, "labels", catalogBigBox)
	generation := f.generation(t, "labels", false)
	schema := fmt.Sprintf("osm_build_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	f.exec(t, "CREATE SCHEMA "+quoted)
	t.Cleanup(func() { _, _ = f.pool.Exec(f.ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	f.exec(t, `INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state)
        VALUES($1,'identity-label-overrides',20,repeat('0',64),'running')`, generation)
	f.exec(t, `SET search_path TO `+quoted+`,public;
        SELECT set_config('workouts_explorer.osm_region_id','fixture:labels',false);
        CREATE TABLE path_segments(segment_id uuid PRIMARY KEY,locality_relation_id bigint,normalized_name text,name text,
            broad_class text,length_m double precision,start_graph_node_id uuid,end_graph_node_id uuid);
        CREATE TABLE attribution_identity_segments(segment_id uuid PRIMARY KEY,group_id uuid,unnamed boolean);
        CREATE TABLE attribution_identity_components(group_id uuid,segment_id uuid,parent_segment_id uuid,PRIMARY KEY(group_id,segment_id));
        WITH input(id,locality,normalized,name,class,length,start_node,end_node,group_name,parent_name) AS (VALUES
            ('east1',1,'homestead','East Homestead Road','road',15,'a','b','east','east1'),
            ('east2',1,'homestead',' east   HOMESTEAD road ','road',20,'b','c','east','east1'),
            ('west',1,'homestead','West Homestead Road','road',40,'d','e','west','west'),
            ('plain',1,'homestead','Homestead Road','road',10,'c','d','plain','plain'),
            ('short',1,'lane','North Lane','road',10,'f','g','north','short'),
            ('long',1,'lane','Lane','road',100,'g','h','lane','long'),
            ('other-city',2,'lane','Other Lane','road',10,'g','i','other','other-city'),
            ('trail',1,'lane','Lane Trail','path',8,'g','j','trail','trail'),
            ('unnamed',1,NULL,NULL,'road',12,'g','k','unnamed','unnamed'))
        INSERT INTO path_segments SELECT md5(id)::uuid,locality,normalized,name,class,length,md5(start_node)::uuid,md5(end_node)::uuid FROM input;
        INSERT INTO attribution_identity_segments SELECT segment_id,
            md5(CASE WHEN name ILIKE '%HOMESTEAD%' AND name NOT LIKE 'West%' AND name<>'Homestead Road' THEN 'east'
                WHEN name='North Lane' THEN 'north' ELSE COALESCE(name,'unnamed') END)::uuid,normalized_name IS NULL FROM path_segments;
        INSERT INTO attribution_identity_components SELECT identity.group_id,segment.segment_id,
            CASE WHEN segment.name ILIKE '%HOMESTEAD%' AND segment.name NOT LIKE 'West%' AND segment.name<>'Homestead Road'
                THEN md5('east1')::uuid ELSE segment.segment_id END
            FROM path_segments segment JOIN attribution_identity_segments identity USING(segment_id);`)
	filename := "identity-label-overrides-batched.sql"
	if optimized {
		filename = "identity-label-overrides-sequential-batched.sql"
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "osm", "batches", filename))
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.NewReplacer(`:"OSM_BUILD_SCHEMA"`, quoted, `:'OSM_GENERATION_ID'`, fmt.Sprintf("'%d'", generation), `:'OSM_REGION_ID'`, "'fixture:labels'",
		"batch_size CONSTANT integer := 250000", "batch_size CONSTANT integer := 2").Replace(string(source))
	for batch := 0; batch < 20; batch++ {
		f.exec(t, sql)
		var done bool
		if err := f.row(`SELECT state='completed' FROM osm_catalog.generation_stages WHERE generation_id=$1 AND stage='identity-label-overrides'`, generation).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		if batch == 19 {
			t.Fatal("label processing did not complete")
		}
	}
	var result []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
        'labels',(SELECT jsonb_agg(to_jsonb(item) ORDER BY group_id,parent_segment_id,scope_id) FROM `+quoted+`.attribution_identity_component_labels item),
        'overrides',(SELECT jsonb_agg(to_jsonb(item) ORDER BY group_id,parent_segment_id,scope_id) FROM `+quoted+`.attribution_identity_component_overrides item))`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Labels    []map[string]any `json:"labels"`
		Overrides []map[string]any `json:"overrides"`
	}
	if err := json.Unmarshal(result, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Labels) != 6 || len(value.Overrides) != 1 || value.Overrides[0]["replacement_name"] != "Lane" {
		t.Fatalf("unexpected scoped labels/unique-neighbor decision: %s", result)
	}
	var eastLength float64
	if err := f.pool.QueryRow(f.ctx, `SELECT length_m FROM `+quoted+`.attribution_identity_component_labels WHERE group_id=md5('east')::uuid`).Scan(&eastLength); err != nil || eastLength != 35 {
		t.Fatalf("cross-batch lengths lost: %v %v", eastLength, err)
	}
	return result
}
