package osm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type regionQueryFixture struct {
	rows  pgx.Rows
	err   error
	calls int
}

func (f *regionQueryFixture) Query(context.Context, string, ...any) (pgx.Rows, error) {
	f.calls++
	return f.rows, f.err
}

type regionRowsFixture struct {
	pgx.Rows
	observations     []pointRegionObservation
	index            int
	scanErr, rowsErr error
	closed           bool
}

func (f *regionRowsFixture) Next() bool {
	f.index++
	return f.index <= len(f.observations)
}

func (f *regionRowsFixture) Scan(dest ...any) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	observation := f.observations[f.index-1]
	*dest[0].(*int) = f.index
	*dest[1].(**string) = observation.regionID
	*dest[2].(**int64) = observation.generation
	*dest[3].(*bool) = observation.inCatalog
	return nil
}

func (f *regionRowsFixture) Err() error { return f.rowsErr }
func (f *regionRowsFixture) Close()     { f.closed = true }

func TestResolveRouteRegionsSharedQuery(t *testing.T) {
	region, generation := "fixture:region", int64(10)
	failure := errors.New("fixture query failure")
	for _, test := range []struct {
		name                       string
		count                      int
		observations               []pointRegionObservation
		queryErr, scanErr, rowsErr error
		wantErr                    bool
	}{
		{name: "empty", count: 0},
		{name: "ready", count: 2, observations: []pointRegionObservation{{&region, &generation, true}, {&region, &generation, true}}},
		{name: "query error", count: 1, queryErr: failure, wantErr: true},
		{name: "scan error", count: 1, observations: []pointRegionObservation{{}}, scanErr: failure, wantErr: true},
		{name: "rows error", count: 1, rowsErr: failure, wantErr: true},
		{name: "incomplete", count: 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := &regionRowsFixture{observations: test.observations, scanErr: test.scanErr, rowsErr: test.rowsErr}
			query := &regionQueryFixture{rows: rows, err: test.queryErr}
			got, err := resolveRouteRegions(context.Background(), query, make([]RouteCoordinate, test.count))
			if (err != nil) != test.wantErr {
				t.Fatalf("readiness=%+v error=%v", got, err)
			}
			if test.count == 0 {
				if query.calls != 0 || got.State != "unavailable" || got.Reason != "no_provider_region" {
					t.Fatalf("empty readiness=%+v query calls=%d", got, query.calls)
				}
			} else if query.calls != 1 || test.queryErr == nil && !rows.closed {
				t.Fatal("lookup did not use one query or close its rows")
			}
			if test.name == "ready" && (got.State != "map_data_ready" || len(got.Regions) != 1 || got.Regions[0].Generation != generation) {
				t.Fatalf("readiness=%+v", got)
			}
			if test.queryErr != nil || test.scanErr != nil || test.rowsErr != nil {
				if !errors.Is(err, failure) {
					t.Fatalf("lost query error: %v", err)
				}
			}
		})
	}
}

func TestResolveRouteRegionsRequiresPool(t *testing.T) {
	if _, err := ResolveRouteRegions(context.Background(), nil, nil); err == nil {
		t.Fatal("default lookup accepted a nil pool")
	}
	if _, err := ResolveRouteRegionsAtPromotion(context.Background(), nil, nil, 1); err == nil {
		t.Fatal("fenced empty lookup accepted a nil pool")
	}
}

func TestResolveRouteRegionsDiscardsLaggedConnectionIntegration(t *testing.T) {
	databaseURL := os.Getenv("OSM_MATCHER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("OSM_MATCHER_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MinConns, config.MinIdleConns, config.MaxConns = 0, 0, 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	physicalConnection := func() *pgx.Conn {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		physical := conn.Conn()
		conn.Release()
		return physical
	}
	old := physicalConnection()
	head, err := PromotionEventHead(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	// Raising only the requested minimum simulates lag without server mutations.
	if _, err := ResolveRouteRegionsAtPromotion(ctx, pool, nil, head+1); !errors.Is(err, ErrMatcherReplicaNotReady) {
		t.Fatalf("lagged lookup error=%v", err)
	}
	if !old.IsClosed() {
		t.Fatal("lagged physical connection was returned to the idle pool")
	}
	fresh := physicalConnection()
	if fresh == old || fresh.IsClosed() {
		t.Fatal("next lookup did not get a fresh physical connection")
	}
	if _, err := ResolveRouteRegionsAtPromotion(ctx, pool, nil, head); err != nil {
		t.Fatalf("caught-up lookup error=%v", err)
	}
	if _, err := ResolveRouteRegions(ctx, pool, nil); err != nil {
		t.Fatalf("default lookup error=%v", err)
	}
	if physicalConnection() != fresh || fresh.IsClosed() {
		t.Fatal("caught-up or default lookup discarded a healthy connection")
	}
}

func TestReadinessFromRegionObservations(t *testing.T) {
	regionA, regionB := "geofabrik:a", "geofabrik:b"
	gen1, gen2, replacement := int64(11), int64(22), int64(23)
	tests := []struct {
		name          string
		observations  []pointRegionObservation
		state, reason string
		regions       []RegionGeneration
	}{
		{"one region", []pointRegionObservation{{&regionA, &gen1, true}, {&regionA, &gen1, true}}, "map_data_ready", "", []RegionGeneration{{regionA, gen1}}},
		{"two regions", []pointRegionObservation{{&regionB, &gen2, true}, {&regionA, &gen1, true}}, "map_data_ready", "", []RegionGeneration{{regionA, gen1}, {regionB, gen2}}},
		{"catalog but inactive", []pointRegionObservation{{nil, nil, true}}, "pending", "region_not_active", nil},
		{"no region", []pointRegionObservation{{nil, nil, false}}, "unavailable", "no_provider_region", nil},
		{"generation replacement", []pointRegionObservation{{&regionB, &replacement, true}}, "map_data_ready", "", []RegionGeneration{{regionB, replacement}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := readinessFromObservations(test.observations)
			if got.State != test.state || got.Reason != test.reason || len(got.Regions) != len(test.regions) {
				t.Fatalf("readiness=%+v", got)
			}
			for i := range test.regions {
				if got.Regions[i] != test.regions[i] {
					t.Fatalf("regions=%+v", got.Regions)
				}
			}
		})
	}
}
