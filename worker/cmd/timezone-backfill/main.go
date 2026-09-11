package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/erhhung/workouts-explorer/internal/workouttimezone"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type candidate struct {
	accountID, workoutID uuid.UUID
	startedAt            time.Time
	offsetMinutes        *int16
	longitude, latitude  float64
}

func main() {
	if err := run(context.Background()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	appURL, osmURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_DATABASE_URL")
	if appURL == "" || osmURL == "" {
		return errors.New("MIGRATION_DATABASE_URL and OSM_DATABASE_URL are required")
	}
	batchSize := 250
	if raw := os.Getenv("TIMEZONE_BACKFILL_BATCH_SIZE"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 1000 {
			return errors.New("TIMEZONE_BACKFILL_BATCH_SIZE must be between 1 and 1000")
		}
		batchSize = value
	}
	appDB, err := pgxpool.New(ctx, appURL)
	if err != nil {
		return fmt.Errorf("connect to application database: %w", err)
	}
	defer appDB.Close()
	osmDB, err := pgxpool.New(ctx, osmURL)
	if err != nil {
		return fmt.Errorf("connect to OSM database: %w", err)
	}
	defer osmDB.Close()

	processed, resolved := 0, 0
	var afterAccount, afterWorkout *uuid.UUID
	for {
		batch, err := readBatch(ctx, appDB, afterAccount, afterWorkout, batchSize)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			var name, release *string
			if err := osmDB.QueryRow(ctx, `SELECT zone.name,dataset.release
				FROM osm_catalog.timezone_datasets dataset
				CROSS JOIN LATERAL (SELECT osm_active.timezone_at($1,$2) AS name) zone
				WHERE dataset.state='active'`, item.longitude, item.latitude).Scan(&name, &release); err != nil {
				return fmt.Errorf("resolve workout %s timezone: %w", item.workoutID, err)
			}
			var offset *int
			if item.offsetMinutes != nil {
				value := int(*item.offsetMinutes)
				offset = &value
			}
			if name != nil && !workouttimezone.MatchesRecordedOffset(*name, item.startedAt, offset) {
				name = nil
			}
			var changed bool
			if name != nil {
				resolved++
			} else {
				release = nil
			}
			if err := appDB.QueryRow(ctx, `SELECT app.set_workout_route_timezone($1,$2,$3,$4)`,
				item.accountID, item.workoutID, name, release).Scan(&changed); err != nil {
				return fmt.Errorf("persist workout %s timezone: %w", item.workoutID, err)
			}
			processed++
		}
		last := batch[len(batch)-1]
		a, w := last.accountID, last.workoutID
		afterAccount, afterWorkout = &a, &w
	}
	var inferred int
	if err := appDB.QueryRow(ctx, `SELECT app.reconcile_all_workout_timezones()`).Scan(&inferred); err != nil {
		return fmt.Errorf("reconcile nearest workout timezones: %w", err)
	}
	_, err = fmt.Fprintf(os.Stdout, "processed %d routed workouts: %d boundary matches, %d nearest-workout updates\n", processed, resolved, inferred)
	return err
}

func readBatch(ctx context.Context, db *pgxpool.Pool, afterAccount, afterWorkout *uuid.UUID, batchSize int) ([]candidate, error) {
	rows, err := db.Query(ctx, `SELECT * FROM app.read_workout_timezone_backfill($1,$2,$3)`, afterAccount, afterWorkout, batchSize)
	if err != nil {
		return nil, fmt.Errorf("read timezone backfill batch: %w", err)
	}
	defer rows.Close()
	result := make([]candidate, 0, batchSize)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.accountID, &item.workoutID, &item.startedAt, &item.offsetMinutes, &item.longitude, &item.latitude); err != nil {
			return nil, fmt.Errorf("scan timezone backfill candidate: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read timezone backfill candidates: %w", err)
	}
	return result, nil
}
