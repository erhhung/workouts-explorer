package api

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/nullable"
)

type activityDisplayRow []any

func (row activityDisplayRow) Scan(destinations ...any) error {
	for index, value := range row {
		if value != nil {
			reflect.ValueOf(destinations[index]).Elem().Set(reflect.ValueOf(value))
		}
	}
	return nil
}

type activityDisplayRows struct {
	pgx.Rows
	rows []activityDisplayRow
	next int
}

func (rows *activityDisplayRows) Next() bool { rows.next++; return rows.next <= len(rows.rows) }
func (rows *activityDisplayRows) Scan(destinations ...any) error {
	return rows.rows[rows.next-1].Scan(destinations...)
}
func (*activityDisplayRows) Close()     {}
func (*activityDisplayRows) Err() error { return nil }

type activityDisplayTx struct {
	pgx.Tx
	rows    []activityDisplayRow
	total   activityDisplayRow
	queries *[]string
}

func (tx activityDisplayTx) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	if tx.queries != nil {
		*tx.queries = append(*tx.queries, query)
	}
	return &activityDisplayRows{rows: tx.rows}, nil
}
func (tx activityDisplayTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if tx.queries != nil {
		*tx.queries = append(*tx.queries, query)
	}
	return tx.total
}

func TestScanWorkoutCanonicalActivityDisplayKeepsRawKey(t *testing.T) {
	for _, label := range []string{"Outdoor Running", "Outdoor Walking", "Outdoor Cycling", "IndoorRunning", "IndoorWalking", "IndoorBike", "Running", "Unknown private activity"} {
		t.Run(label, func(t *testing.T) {
			id, sourceID, typeID := uuid.New(), uuid.New(), uuid.New()
			key := healthautoexport.NormalizedTypeKey(label)
			row := make(activityDisplayRow, 37)
			row[0], row[1], row[2], row[3], row[4] = id, sourceID, typeID, key, label
			indoor := true
			row[12] = &indoor
			workout, err := scanWorkout(row)
			if err != nil || workout.Id != compactUUID(id) || workout.SourceId != compactUUID(sourceID) || workout.Type.Id != compactUUID(typeID) ||
				workout.Type.Key != key || workout.Type.DisplayName != healthautoexport.CanonicalActivityLabel(label) {
				t.Fatalf("workout=%+v err=%v", workout, err)
			}
		})
	}
}

func TestSummaryCanonicalActivityDisplayDoesNotMergeAliasIdentities(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	firstKey, secondKey := healthautoexport.NormalizedTypeKey("Outdoor Bike"), healthautoexport.NormalizedTypeKey("Outdoor Cycling")
	zero := "0"
	tx := activityDisplayTx{
		rows: []activityDisplayRow{
			{first, firstKey, "Outdoor Bike", int64(1), "60", nil, nil, int64(0), nil, int64(0)},
			{second, secondKey, "Outdoor Cycling", int64(2), "120", nil, nil, int64(0), nil, int64(0)},
		},
		total: activityDisplayRow{stringPointer("180"), nil, nil, int64(0), &zero, int64(0)},
	}
	items, totals, err := querySummary(context.Background(), tx, resolvedRange{})
	if err != nil || len(items) != 2 || totals.Count != 3 {
		t.Fatalf("summary=%+v totals=%+v err=%v", items, totals, err)
	}
	for index, id := range []uuid.UUID{first, second} {
		if items[index].Type.Id != compactUUID(id) || items[index].Type.Key != []string{firstKey, secondKey}[index] || items[index].Type.DisplayName != "Outdoor Cycle" {
			t.Fatalf("alias identity was changed: %+v", items[index])
		}
	}
}

func TestSummaryMeasuredDistanceCountQueryAndScan(t *testing.T) {
	for _, test := range []struct {
		name                             string
		count, distanceCount, routeCount int64
		distance                         *string
	}{
		{"route-less measured subset", 3, 2, 0, stringPointer("12.00000000000000000001")},
		{"measured zero", 1, 1, 0, stringPointer("0")},
		{"routed without distance", 1, 0, 1, nil},
		{"empty", 0, 0, 0, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var queries []string
			tx := activityDisplayTx{
				total:   activityDisplayRow{stringPointer("0"), test.distance, nil, test.routeCount, nil, test.distanceCount},
				queries: &queries,
			}
			if test.count > 0 {
				tx.rows = []activityDisplayRow{{uuid.New(), "novel_activity", "Novel Activity", test.count, "0", test.distance, nil, test.routeCount, nil, test.distanceCount}}
			}
			items, totals, err := querySummary(context.Background(), tx, resolvedRange{})
			if err != nil || totals.Count != test.count || totals.DistanceCount != test.distanceCount || totals.RouteCount != test.routeCount || totals.Duration != "0" {
				t.Fatalf("items=%+v totals=%+v err=%v", items, totals, err)
			}
			if len(queries) != 2 {
				t.Fatalf("expected two summary queries, got %d", len(queries))
			}
			for _, query := range queries {
				for _, fragment := range []string{"count(metrics.distance)::bigint", "max(value) FILTER (WHERE metric='distance' AND unit='km') AS distance", "trim_scale(sum(metrics.distance))::text", "metric='total_energy' AND unit='kcal'", "count(route.workout_id)::bigint", "sum(metrics.distance) FILTER (WHERE route.workout_id IS NOT NULL)"} {
					if !strings.Contains(query, fragment) {
						t.Errorf("summary query missing %q", fragment)
					}
				}
			}
			if test.count > 0 {
				if len(items) != 1 || items[0].Totals.DistanceCount != test.distanceCount || items[0].Totals.RouteCount != test.routeCount {
					t.Fatalf("per-type scan=%+v", items)
				}
				if test.distance == nil {
					if !totals.Distance.IsNull() || !items[0].Totals.Distance.IsNull() {
						t.Fatal("unmeasured distance must remain null")
					}
				} else {
					for _, value := range []generated.SummaryTotals{totals, items[0].Totals} {
						metric, err := value.Distance.Get()
						if err != nil || metric.Value != *test.distance || metric.Unit != "km" {
							t.Fatalf("distance=%+v err=%v", metric, err)
						}
					}
				}
			} else {
				for _, metric := range []nullable.Nullable[generated.ExactMetric]{totals.Distance, totals.Energy, totals.RoutedDistance} {
					value, err := metric.Get()
					if err != nil || value.Value != "0" {
						t.Fatalf("empty metric=%+v err=%v", value, err)
					}
				}
			}
		})
	}
}

func TestMapSelectionCanonicalActivityDisplayKeepsRawKey(t *testing.T) {
	for _, label := range []string{"Outdoor Running", "Outdoor Walking", "Outdoor Cycling", "IndoorRunning", "IndoorWalking", "IndoorBike", "Cycling", "Unknown private activity"} {
		t.Run(label, func(t *testing.T) {
			workoutID, typeID := uuid.New(), uuid.New()
			key := healthautoexport.NormalizedTypeKey(label)
			tx := activityDisplayTx{rows: []activityDisplayRow{{
				workoutID, typeID, key, label, time.Time{}, time.Time{}, "60", nil,
				float64(-122), float64(37), float64(-121), float64(38),
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, false,
			}}}
			selection, err := readMapSelection(context.Background(), tx, uuid.New(), uuid.New(), 1, time.Time{}, resolvedRange{}, nil)
			if err != nil || len(selection.Workouts) != 1 {
				t.Fatalf("selection=%+v err=%v", selection, err)
			}
			got := selection.Workouts[0]
			if got.Id != compactUUID(workoutID) || got.Type.Id != compactUUID(typeID) || got.Type.Key != key || got.Type.Name != healthautoexport.CanonicalActivityLabel(label) {
				t.Fatalf("map display/identity=%+v", got)
			}
		})
	}
}
