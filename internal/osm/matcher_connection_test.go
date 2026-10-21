package osm

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestMatcherOwnedConnectionCleanup(t *testing.T) {
	matcherConnectionFixtureEnvironment(t)
	for _, finish := range []string{"close", "commit", "rollback"} {
		t.Run(finish, func(t *testing.T) {
			pool, _ := matcherReplicaTestPool(t, 2)
			acquired, err := pool.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			conn := acquired.Hijack()
			snapshot, err := BeginMatcherSnapshotOnConnection(context.Background(), conn, []MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.tx.Conn() != conn || snapshot.PhysicalConnectionClosed() {
				t.Fatal("snapshot did not retain its dedicated connection")
			}
			switch finish {
			case "commit":
				err = snapshot.Commit(context.Background())
			case "rollback":
				err = snapshot.Rollback(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := snapshot.Close(ctx); err != nil || !conn.IsClosed() || !snapshot.PhysicalConnectionClosed() {
				t.Fatalf("canceled cleanup: closed=%v err=%v", conn.IsClosed(), err)
			}
			if err := snapshot.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := (*MatcherSnapshot)(nil).Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*MatcherSnapshot)(nil).PhysicalConnectionClosed() || (&MatcherSnapshot{}).PhysicalConnectionClosed() {
		t.Fatal("nil or unowned snapshot claimed dedicated physical closure")
	}
}

func TestMatcherConnectionFailureOwnership(t *testing.T) {
	matcherConnectionFixtureEnvironment(t)
	pool, _ := matcherReplicaTestPool(t, 3)
	acquired, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn := acquired.Hijack()
	defer conn.Close(context.Background())
	snapshot, err := BeginMatcherSnapshotOnConnection(context.Background(), conn, []MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}}, false)
	if snapshot != nil || !errors.Is(err, ErrMatcherGenerationChanged) || conn.IsClosed() {
		t.Fatalf("failed constructor took connection ownership: snapshot=%v closed=%v err=%v", snapshot, conn.IsClosed(), err)
	}
	if conn.PgConn().TxStatus() != 'I' {
		t.Fatal("failed constructor retained transaction")
	}
	if _, err := BeginMatcherSnapshotOnConnection(context.Background(), nil, nil, true); err == nil || errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal("missing targets not validated")
	}
}

func matcherConnectionFixtureEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"PGSERVICE", "PGSERVICEFILE", "PGSSLKEY", "PGSSLCERT", "PGSSLROOTCERT"} {
		t.Setenv(key, "")
	}
	t.Setenv("PGPASSFILE", "/dev/null")
	t.Setenv("PGPASSWORD", "fixture-password")
}
