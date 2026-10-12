package api

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	rows  []activityDisplayRow
	total activityDisplayRow
}

func (tx activityDisplayTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &activityDisplayRows{rows: tx.rows}, nil
}
func (tx activityDisplayTx) QueryRow(context.Context, string, ...any) pgx.Row { return tx.total }

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
			{first, firstKey, "Outdoor Bike", int64(1), "60", nil, nil, int64(0), nil},
			{second, secondKey, "Outdoor Cycling", int64(2), "120", nil, nil, int64(0), nil},
		},
		total: activityDisplayRow{stringPointer("180"), nil, nil, int64(0), &zero},
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
