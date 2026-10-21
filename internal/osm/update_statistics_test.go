package osm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type statisticsDBStub struct {
	tx       *statisticsTxStub
	beginErr error
	ctx      context.Context
}

func (s *statisticsDBStub) Begin(ctx context.Context) (pgx.Tx, error) {
	s.ctx = ctx
	return s.tx, s.beginErr
}

type statisticsTxStub struct {
	pgx.Tx
	events      []string
	failAt      string
	denied      string
	err         error
	cancel      context.CancelFunc
	rollbackErr error
	rollbackTTL time.Duration
}

func (s *statisticsTxStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.events = append(s.events, sql)
	if sql == s.failAt {
		if s.cancel != nil {
			s.cancel()
		}
		return pgconn.CommandTag{}, s.err
	}
	return pgconn.CommandTag{}, nil
}

func (s *statisticsTxStub) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	s.events = append(s.events, sql)
	return statisticsRowStub{s}
}

type statisticsRowStub struct{ tx *statisticsTxStub }

func (r statisticsRowStub) Scan(dest ...any) error {
	if r.tx.failAt == updateStatisticsPrivileges {
		return r.tx.err
	}
	*dest[0].(*string) = r.tx.denied
	return nil
}

func (s *statisticsTxStub) Commit(context.Context) error {
	s.events = append(s.events, "commit")
	if s.failAt == "commit" {
		return s.err
	}
	return nil
}

func (s *statisticsTxStub) Rollback(ctx context.Context) error {
	s.events = append(s.events, "rollback")
	s.rollbackErr = ctx.Err()
	deadline, _ := ctx.Deadline()
	s.rollbackTTL = time.Until(deadline)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func TestUpdateStatisticsSQLContract(t *testing.T) {
	want := []string{
		"ANALYZE osm_canonical.ways (region_id,generation_id,way_id,version)",
		"ANALYZE osm_canonical.localities (region_id,generation_id,relation_id,relation_version)",
		"ANALYZE osm_canonical.path_segments (region_id,generation_id,segment_id,source_way_id,source_way_version,logical_path_id,start_graph_node_id,end_graph_node_id)",
		"ANALYZE osm_catalog.generations (id,region_id,state)",
	}
	if !reflect.DeepEqual(updateStatisticsStatements, want) {
		t.Fatalf("targeted recursive statistics contract changed: %v", updateStatisticsStatements)
	}
	for _, statement := range want {
		if strings.Contains(statement, "ONLY") {
			t.Fatalf("leaf statistics would be skipped: %s", statement)
		}
	}
}

func TestUpdateStatisticsTransactionOrderingAndBounds(t *testing.T) {
	for _, callerTimeout := range []time.Duration{time.Hour, time.Second} {
		ctx, cancel := context.WithTimeout(context.Background(), callerTimeout)
		tx := &statisticsTxStub{}
		db := &statisticsDBStub{tx: tx}
		if err := refreshUpdateStatistics(ctx, db); err != nil {
			t.Fatal(err)
		}
		deadline, ok := db.ctx.Deadline()
		limit := min(callerTimeout, 30*time.Minute)
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > limit {
			t.Fatalf("maintenance deadline=%v, want <=%v", deadline, limit)
		}
		if !errors.Is(db.ctx.Err(), context.Canceled) {
			t.Fatal("maintenance context was not cleaned up")
		}
		want := []string{`SET LOCAL statement_timeout='10min'; SET LOCAL lock_timeout='5s'`, updateStatisticsPrivileges}
		want = append(want, updateStatisticsStatements...)
		want = append(want, "commit", "rollback")
		if !reflect.DeepEqual(tx.events, want) {
			t.Fatalf("maintenance transaction events=%v", tx.events)
		}
		if tx.rollbackTTL <= 0 || tx.rollbackTTL > 5*time.Second {
			t.Fatalf("unbounded cleanup: %v", tx.rollbackTTL)
		}
		cancel()
	}
}

func TestUpdateStatisticsFailuresRollbackAndRetry(t *testing.T) {
	failures := []string{"begin", `SET LOCAL statement_timeout='10min'; SET LOCAL lock_timeout='5s'`, updateStatisticsPrivileges, "denied", "commit"}
	failures = append(failures, updateStatisticsStatements...)
	for _, failure := range failures {
		t.Run(failure, func(t *testing.T) {
			sentinel := errors.New("maintenance unavailable")
			tx := &statisticsTxStub{failAt: failure, err: sentinel}
			db := &statisticsDBStub{tx: tx}
			if failure == "begin" {
				db.beginErr = sentinel
			}
			if failure == "denied" {
				tx.denied = "osm_canonical.ways_g42"
			}
			err := refreshUpdateStatistics(context.Background(), db)
			if failure == "denied" {
				if err == nil || !strings.Contains(err.Error(), tx.denied) {
					t.Fatalf("silently accepted skipped leaf statistics: %v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("error=%v, want %v", err, sentinel)
			}
			if failure == "begin" {
				if len(tx.events) != 0 {
					t.Fatalf("used failed transaction: %v", tx.events)
				}
			} else if tx.events[len(tx.events)-1] != "rollback" || failure != "commit" && contains(tx.events, "commit") {
				t.Fatalf("failed maintenance committed or did not clean up: %v", tx.events)
			}
			tx.failAt, tx.denied, db.beginErr = "", "", nil
			tx.events = nil
			if err := refreshUpdateStatistics(context.Background(), db); err != nil || !contains(tx.events, "commit") {
				t.Fatalf("retry failed: %v; events=%v", err, tx.events)
			}
		})
	}
}

func TestUpdateStatisticsCancellationKeepsCleanupBoundedAndUncanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx := &statisticsTxStub{failAt: updateStatisticsStatements[0], err: context.Canceled, cancel: cancel}
	if err := refreshUpdateStatistics(ctx, &statisticsDBStub{tx: tx}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want cancellation", err)
	}
	if tx.rollbackErr != nil || tx.rollbackTTL <= 0 || tx.rollbackTTL > 5*time.Second || contains(tx.events, "commit") {
		t.Fatalf("cancellation cleanup error=%v TTL=%v events=%v", tx.rollbackErr, tx.rollbackTTL, tx.events)
	}
}
