package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestAllTimeRangeIsUnfiltered(t *testing.T) {
	shortcut := generated.AllTimeRange
	zone := "Pacific/Kiritimati"
	rangeValue, field, err := resolveRange(rangeInput{shortcut: &shortcut, timezone: &zone}, readPreferences{timezone: "UTC"}, time.Date(2026, 12, 31, 11, 0, 0, 0, time.UTC))
	if err != nil || field != "" || !rangeValue.allTime || rangeValue.start.Format(time.DateOnly) != "2027-01-01" {
		t.Fatalf("All time resolution=%+v field=%s error=%v", rangeValue, field, err)
	}
	filter, arguments := rangeValue.filter("w.local_start_date", 1)
	if filter != "" || len(arguments) != 0 {
		t.Fatalf("unfiltered mode emitted date bounds: %q %v", filter, arguments)
	}
	if !validDateRangePreference("allTime") {
		t.Fatal("All time preference rejected")
	}
}

type allTimeSelectionTx struct {
	pgx.Tx
	query string
	args  []any
}

func (tx *allTimeSelectionTx) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.query, tx.args = query, args
	return pgconn.NewCommandTag("INSERT 0 5003"), nil
}

func TestAllTimeSelectionDoesNotFilterOrTruncateRoutes(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "checked subset"}[explicit], func(t *testing.T) {
			tx := &allTimeSelectionTx{}
			count, err := insertMapSelectionWorkouts(context.Background(), tx, uuid.New(), uuid.New(), resolvedRange{allTime: true}, []uuid.UUID{uuid.New()}, explicit)
			if err != nil || count != 5003 || strings.Contains(tx.query, "local_start_date") || strings.Contains(tx.query, "LIMIT") {
				t.Fatalf("All time selection filtered or truncated: count=%d error=%v SQL=%s", count, err, tx.query)
			}
			wantArgs := 2
			if explicit {
				wantArgs++
				if !strings.Contains(tx.query, "workout.id=ANY($3::uuid[])") {
					t.Fatal("checked membership filter missing or incorrectly parameterized")
				}
			}
			if len(tx.args) != wantArgs {
				t.Fatal("unused date arguments remain in unfiltered route query")
			}
		})
	}
}
