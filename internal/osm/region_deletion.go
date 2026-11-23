package osm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Region deletion retires metadata but keeps promotion/build history. Physical
// partitions, indexes, TOAST and scratch schemas are dropped, not row-deleted.
func (s PostgreSQLUpdateStore) DeleteRegion(ctx context.Context, request, region string, reporter UpdateReporter) error {
	unlock, err := s.LockUpdater(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	steps := []func(context.Context) error{
		func(ctx context.Context) error { return s.detachDeletedRegion(ctx, request, region) },
		func(ctx context.Context) error { return s.processStorageGC(ctx, &region) },
		func(ctx context.Context) error {
			reclaimed, err := regionStorageReclaimed(ctx, s.Pool, region)
			if err != nil {
				return err
			}
			if !reclaimed {
				return errors.New("region storage reclamation is incomplete")
			}
			_, err = s.Pool.Exec(ctx, `UPDATE osm_catalog.region_deletions SET state='completed',database_bytes_after=0,finished_at=COALESCE(finished_at,clock_timestamp()) WHERE id=$1 AND region_id=$2`, request, region)
			return err
		},
	}
	var complete bool
	if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM osm_catalog.region_deletions WHERE id=$1 AND region_id=$2 AND state='completed')`, request, region).Scan(&complete); err != nil {
		return err
	}
	if complete {
		return nil
	}
	for i, step := range steps {
		if reporter != nil {
			if err = reporter.Report(ctx, UpdateProgress{Type: "start", Task: i + 1}); err != nil {
				return err
			}
		}
		started := time.Now()
		if err = step(ctx); err != nil {
			return err
		}
		if reporter != nil {
			amount := time.Since(started).Milliseconds()
			if err = reporter.Report(ctx, UpdateProgress{Type: "complete", Task: i + 1, Completed: true, ElapsedMilliseconds: &amount}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s PostgreSQLUpdateStore) detachDeletedRegion(ctx context.Context, request, region string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, region); err != nil {
		return err
	}
	var boundRegion string
	err = tx.QueryRow(ctx, `SELECT region_id FROM osm_catalog.region_deletions WHERE id=$1 FOR UPDATE`, request).Scan(&boundRegion)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if err == nil && boundRegion != region {
		return errors.New("region deletion identity mismatch")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO osm_catalog.region_deletions(id,region_id,state,database_bytes_before) VALUES($1,$2,'reclaiming',COALESCE(osm_catalog.region_database_bytes($2),0)) ON CONFLICT(id) DO NOTHING`, request, region); err != nil {
		return err
	}
	var build int64
	var ways, localities, segments string
	err = tx.QueryRow(ctx, `SELECT generation_id,ways_relation::text,localities_relation::text,path_segments_relation::text FROM osm_catalog.region_storage WHERE region_id=$1 FOR UPDATE`, region).Scan(&build, &ways, &localities, &segments)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if err == nil {
		for _, relation := range []struct{ parent, name string }{{"path_segments", segments}, {"localities", localities}, {"ways", ways}} {
			if relation.name != fmt.Sprintf("%s_g%d", relation.parent, build) {
				return errors.New("region partition identity mismatch")
			}
			if _, err = tx.Exec(ctx, "ALTER TABLE "+pgx.Identifier{"osm_canonical", relation.parent}.Sanitize()+" DETACH PARTITION "+pgx.Identifier{"osm_canonical", relation.name}.Sanitize()); err != nil {
				return err
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT id,schema_name::text FROM osm_catalog.generations WHERE region_id=$1 ORDER BY id FOR UPDATE`, region)
	if err != nil {
		return err
	}
	type resource struct {
		id     int64
		schema string
	}
	var resources []resource
	for rows.Next() {
		var r resource
		if err = rows.Scan(&r.id, &r.schema); err != nil {
			rows.Close()
			return err
		}
		resources = append(resources, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range resources {
		if r.schema != fmt.Sprintf("osm_build_%d", r.id) {
			return errors.New("region build identity mismatch")
		}
		for _, parent := range []string{"path_segments", "localities", "ways"} {
			name := fmt.Sprintf("%s_g%d", parent, r.id)
			if _, err = tx.Exec(ctx, `INSERT INTO osm_catalog.storage_gc(region_id,generation_id,object_kind,schema_name,object_name)
                SELECT $1,$2,'relation','osm_canonical',$3::text::name WHERE to_regclass(format('osm_canonical.%I',$3::text)) IS NOT NULL
                ON CONFLICT(object_kind,schema_name,object_name) DO UPDATE SET state='queued',finished_at=NULL,failure_summary=NULL`, region, r.id, name); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO osm_catalog.storage_gc(region_id,generation_id,object_kind,schema_name)
            SELECT $1,$2,'build_schema',$3::text::name WHERE to_regnamespace($3::text) IS NOT NULL
            ON CONFLICT(object_kind,schema_name,object_name) DO UPDATE SET state='queued',finished_at=NULL,failure_summary=NULL`, region, r.id, r.schema); err != nil {
			return err
		}
	}
	for _, query := range []string{`DELETE FROM osm_catalog.region_storage WHERE region_id=$1`,
		`UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE region_id=$1 AND state='active'`,
		`UPDATE osm_catalog.generations SET state='failed',failure_summary='Removed by administrator region deletion.' WHERE region_id=$1 AND state IN('building','validating','evaluating')`} {
		if _, err = tx.Exec(ctx, query, region); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func regionStorageReclaimed(ctx context.Context, pool *pgxpool.Pool, region string) (bool, error) {
	var reclaimed bool
	err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM osm_catalog.region_storage WHERE region_id=$1)
        AND NOT EXISTS(SELECT 1 FROM osm_catalog.generations g WHERE region_id=$1 AND(
            state IN('active','building','validating','evaluating') OR to_regnamespace(schema_name::text) IS NOT NULL
            OR to_regclass(format('osm_canonical.ways_g%s',id)) IS NOT NULL
            OR to_regclass(format('osm_canonical.localities_g%s',id)) IS NOT NULL
            OR to_regclass(format('osm_canonical.path_segments_g%s',id)) IS NOT NULL))
        AND NOT EXISTS(SELECT 1 FROM osm_catalog.storage_gc WHERE region_id=$1 AND
            (state<>'dropped' OR CASE WHEN object_kind='build_schema' THEN to_regnamespace(schema_name::text) IS NOT NULL ELSE to_regclass(format('%I.%I',schema_name,object_name)) IS NOT NULL END))`, region).Scan(&reclaimed)
	return reclaimed, err
}

func RegionDeletionComplete(ctx context.Context, pool *pgxpool.Pool, request, region string) (bool, error) {
	var complete bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM osm_catalog.region_deletions WHERE id=$1 AND region_id=$2 AND state='completed' AND database_bytes_after=0)`, request, region).Scan(&complete); err != nil || !complete {
		return false, err
	}
	return regionStorageReclaimed(ctx, pool, region)
}
