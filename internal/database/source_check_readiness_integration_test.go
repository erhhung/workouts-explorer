package database

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSourceCheckReadinessIntegration(t *testing.T) {
	if os.Getenv("M10_ADMIN_TEST_DISPOSABLE") != "1" {
		t.Skip("requires an owned disposable database")
	}
	ctx := context.Background()
	var pools []*pgxpool.Pool
	for _, name := range []string{"MIGRATION_DATABASE_URL", "API_DATABASE_URL", "WORKER_DATABASE_URL", "COVERAGE_WORKER_DATABASE_URL"} {
		config, err := pgxpool.ParseConfig(os.Getenv(name))
		if err != nil || config.ConnConfig.Host != "127.0.0.1" || !strings.HasPrefix(config.ConnConfig.Database, "workouts_admin_test_") {
			t.Fatal("requires a named loopback scratch database")
		}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
	}
	owner, api, worker, coverage := pools[0], pools[1], pools[2], pools[3]
	for _, pool := range []*pgxpool.Pool{api, worker, coverage} {
		if !Ready(ctx, pool) {
			t.Fatal("fresh additive schema33 runtime contract is not ready")
		}
	}
	for _, pool := range []*pgxpool.Pool{api, coverage} {
		if _, err := pool.Exec(ctx, `SELECT * FROM app.claim_next_source_check('invalid-role',gen_random_uuid(),interval '1 minute',32)`); err == nil {
			t.Fatal("private source checks admitted a non-worker role")
		}
	}
	var count int
	if err := worker.QueryRow(ctx, `SELECT count(*) FROM app.claim_next_source_check('old-runtime',gen_random_uuid(),interval '1 minute',31)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old-runtime claim=%d err=%v", count, err)
	}
	if _, err := owner.Exec(ctx, `GRANT EXECUTE ON FUNCTION app.claim_next_source_check(text,uuid,interval,integer) TO workouts_api`); err != nil {
		t.Fatal(err)
	}
	defer owner.Exec(ctx, `REVOKE EXECUTE ON FUNCTION app.claim_next_source_check(text,uuid,interval,integer) FROM workouts_api`)
	if Ready(ctx, worker) || Ready(ctx, api) {
		t.Fatal("readiness accepted an overprivileged claim ABI")
	}
	if _, err := owner.Exec(ctx, `REVOKE EXECUTE ON FUNCTION app.claim_next_source_check(text,uuid,interval,integer) FROM workouts_api`); err != nil {
		t.Fatal(err)
	}
	if !Ready(ctx, worker) {
		t.Fatal("restored claim privileges did not restore readiness")
	}
}
