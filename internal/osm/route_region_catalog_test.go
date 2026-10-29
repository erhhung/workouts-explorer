package osm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRouteCatalogReplicaFences(t *testing.T) {
	matcherConnectionFixtureEnvironment(t)
	for _, test := range []struct {
		name              string
		revision, eventID int64
		lagged            bool
	}{
		{"same promotion older catalog", 2, 7, true},
		{"older promotion", 1, 8, true},
		{"current", 1, 7, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, connected := matcherReplicaTestPool(t, 1)
			defer pool.Close()
			catalog, err := ReadRouteRegionCatalog(context.Background(), pool, test.eventID, test.revision)
			physical := <-connected
			if test.lagged {
				if !errors.Is(err, ErrMatcherReplicaNotReady) || !physical.IsClosed() || catalog.Revision != 0 {
					t.Fatalf("lagged proof escaped: catalog=%+v err=%v closed=%v", catalog, err, physical.IsClosed())
				}
				// A new acquisition reaches a caught-up node, rather than the
				// lagged LIFO physical connection. No primary fallback exists.
				if test.eventID == 7 {
					catalog, err = ReadRouteRegionCatalog(context.Background(), pool, 7, 2)
					if err != nil || catalog.Revision != 2 {
						t.Fatalf("fresh proof=%+v err=%v", catalog, err)
					}
				}
				return
			}
			if err != nil || physical.IsClosed() || catalog.Revision != 1 || catalog.PromotionEventID != 7 ||
				catalog.Fingerprint != strings.Repeat("01", 32) || catalog.SelectionFingerprint != strings.Repeat("02", 32) ||
				catalog.ActiveGenerations["fixture:unconfigured"] != 3 || len(catalog.ConfiguredRegions) != 1 ||
				catalog.ConfiguredRegions[0].DisplayName != "Region" {
				t.Fatalf("catalog=%+v err=%v", catalog, err)
			}
		})
	}
}

func TestRouteReadinessCatalogProof(t *testing.T) {
	matcherConnectionFixtureEnvironment(t)
	for _, points := range [][]RouteCoordinate{nil, {{Longitude: 1, Latitude: 2}}} {
		pool, _ := matcherReplicaTestPool(t, 2)
		got, err := ResolveRouteRegionsWithCatalog(context.Background(), pool, points, 7, 2)
		if err != nil || got.CatalogRevision != 2 || got.CatalogFingerprint != strings.Repeat("01", 32) ||
			got.SelectionFingerprint != strings.Repeat("02", 32) {
			t.Fatalf("readiness=%+v err=%v", got, err)
		}
		if len(points) == 0 && (got.State != "unavailable" || got.Reason != "no_provider_region") ||
			len(points) > 0 && (got.State != "map_data_ready" || len(got.Regions) != 1) {
			t.Fatalf("point/gap semantics changed: %+v", got)
		}
		pool.Close()
	}
	pool, connected := matcherReplicaTestPool(t, 1)
	defer pool.Close()
	if _, err := ResolveRouteRegionsWithCatalog(context.Background(), pool, nil, 7, 2); !errors.Is(err, ErrMatcherReplicaNotReady) {
		t.Fatalf("empty route bypassed catalog fencing: %v", err)
	}
	if !(<-connected).IsClosed() {
		t.Fatal("lagged empty-route connection was retained")
	}
}

type routeCatalogRow struct {
	revision, eventID                         int64
	fingerprint, selection, heads, configured []byte
	err                                       error
}

func (r routeCatalogRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*int64), *dest[5].(*int64) = r.revision, r.eventID
	*dest[1].(*[]byte), *dest[2].(*[]byte) = r.fingerprint, r.selection
	*dest[3].(*[]byte), *dest[4].(*[]byte) = r.heads, r.configured
	return nil
}

type routeCatalogTx struct {
	pgx.Tx
	row   routeCatalogRow
	calls int
}

func (tx *routeCatalogTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if sql != readRouteRegionCatalogSQL || len(args) != 0 {
		panic("unexpected catalog read")
	}
	tx.calls++
	return tx.row
}

func TestRouteCatalogRejectsMissingMetadata(t *testing.T) {
	for _, name := range []string{"missing row", "old schema", "revision", "full fingerprint", "selection fingerprint", "heads", "configured", "null heads", "null configured", "negative promotion"} {
		t.Run(name, func(t *testing.T) {
			row := routeCatalogRow{revision: 1, fingerprint: make([]byte, 32), selection: make([]byte, 32), heads: []byte(`{}`), configured: []byte(`[]`)}
			switch name {
			case "missing row":
				row.err = pgx.ErrNoRows
			case "old schema":
				row.err = errors.New("relation does not exist")
			case "revision":
				row.revision = 0
			case "full fingerprint":
				row.fingerprint = nil
			case "selection fingerprint":
				row.selection = nil
			case "heads":
				row.heads = []byte(`[]`)
			case "configured":
				row.configured = []byte(`{}`)
			case "null heads":
				row.heads = []byte(`null`)
			case "null configured":
				row.configured = []byte(`null`)
			case "negative promotion":
				row.eventID = -1
			}
			tx := &routeCatalogTx{row: row}
			if _, err := readRouteRegionCatalog(context.Background(), tx, 0, 0); err == nil || tx.calls != 1 {
				t.Fatalf("missing metadata accepted or fallback attempted: err=%v calls=%d", err, tx.calls)
			}
		})
	}
}

func TestRouteCatalogReadAndROISQLStayCheap(t *testing.T) {
	for _, forbidden := range []string{"ST_", "sha256", "osm_catalog.regions", "osm_catalog.generations", "string_agg"} {
		if strings.Contains(readRouteRegionCatalogSQL, forbidden) {
			t.Fatalf("steady-state proof read contains %q", forbidden)
		}
	}
	if strings.Contains(resolveRouteRegionsSQL, "ST_Area") || strings.Contains(resolveRouteRegionsSQL, "MATERIALIZED") ||
		strings.Count(resolveRouteRegionsSQL, "ST_Covers(region.boundary,point.location)") != 2 ||
		strings.Count(resolveRouteRegionsSQL, "ORDER BY region.geographic_area_m2,region.id LIMIT 1") != 2 {
		t.Fatal("ROI must retain indexable base-table predicates and stored area/id ordering")
	}
	if _, err := ReadRouteRegionCatalog(context.Background(), nil, 0, 0); err == nil {
		t.Fatal("catalog accepted a missing pool")
	}
	if _, err := ResolveRouteRegionsWithCatalog(context.Background(), nil, nil, 0, 0); err == nil {
		t.Fatal("proof lookup accepted a missing pool")
	}
}
