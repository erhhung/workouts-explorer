package api

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestJobRegionalGenerationLabelsIntegration(t *testing.T) {
	apiURL, ownerURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if apiURL == "" || ownerURL == "" {
		t.Skip("explicit disposable API fixture required")
	}
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	reader, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_, account := insertSourceTestUser(t, owner)
	parent, child, workout, activity := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx := integrationAccountTransaction(t, owner, account)
	defer tx.Rollback(ctx)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`SET LOCAL session_replication_role='replica'`)
	exec(`INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'running','Outdoor Run')`, activity, account)
	exec(`INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,
		provider_label,started_at,ended_at,provider_duration) VALUES($1,$2,$3,$4,$5,'generation-label',decode(repeat('01',32),'hex'),
		'Outdoor Run','2026-11-14Z','2026-11-14Z',0)`, workout, account, uuid.New(), uuid.New(), activity)
	exec(`INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'coverage_update',20)`, parent, account)
	exec(`INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
		rules_version,sampling_version,path_policy_version,minimum_traversal_meters) VALUES($1,$2,'geofabrik:label-north',62,1,
		'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v83',5)`, parent, account)
	exec(`INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority) VALUES($1,$2,$3,'coverage_update_route',20)`, child, parent, account)
	exec(`INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
		VALUES($1,$2,$3,1,decode(repeat('01',32),'hex'),'[{"regionId":"geofabrik:label-north","generation":62},
		{"regionId":"geofabrik:label-south","generation":65},{"regionId":"geofabrik:label-unknown","generation":70}]')`, child, account, workout)
	exec(`INSERT INTO app.osm_region_catalog(id,provider,payload) VALUES
		('geofabrik:label-north','geofabrik','{"generation":19,"buildId":80,"promotionNumbers":{"62":18,"40":12,"80":19}}'),
		('geofabrik:label-south','geofabrik','{"generation":null,"buildId":null,"promotionNumbers":{"65":1}}'),
		('geofabrik:label-unknown','geofabrik','{"generation":7,"buildId":90}')
		ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload`)
	exec(`INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
		VALUES($1,$2,'geofabrik:label-north',62)`, account, workout)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	read := integrationAccountTransaction(t, reader, account)
	defer read.Rollback(ctx)
	detail, _, err := scanJobDetail(read.QueryRow(ctx, jobDetailSelect+` WHERE j.id=$1`, child))
	if err != nil {
		t.Fatal(err)
	}
	labels := detail.CoverageRoute.TargetGenerations
	if len(labels) != 3 || labels[0].Generation != 62 || labels[1].Generation != 65 || labels[2].Generation != 70 {
		t.Fatal("build provenance changed", labels)
	}
	first, firstErr := labels[0].RegionalGeneration.Get()
	second, secondErr := labels[1].RegionalGeneration.Get()
	if firstErr != nil || first != 18 || secondErr != nil || second != 1 || !labels[2].RegionalGeneration.IsNull() {
		t.Fatal("labels substituted current generation or internal build ID", labels)
	}
	if !labels[0].Latest || labels[1].Latest || labels[2].Latest {
		t.Fatal("presentation changed target freshness", labels)
	}
}
