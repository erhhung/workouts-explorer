package evaluator

import (
	"context"
	"fmt"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgreSQLSource struct{ Pool *pgxpool.Pool }

func (s PostgreSQLSource) ResolveAccount(ctx context.Context, identity string) (uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `SELECT account_user.account_id
		FROM app.authentication_principals principal
		JOIN app.users account_user ON account_user.principal_id=principal.id
		WHERE principal.canonical_username=$1 OR principal.canonical_email=$1
		ORDER BY account_user.account_id LIMIT 2`, identity)
	if err != nil {
		return uuid.Nil, fmt.Errorf("query account")
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, fmt.Errorf("scan account")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, fmt.Errorf("read account")
	}
	if len(ids) == 0 {
		return uuid.Nil, &AccountResolutionError{Kind: "not_found"}
	}
	if len(ids) != 1 {
		return uuid.Nil, &AccountResolutionError{Kind: "ambiguous"}
	}
	return ids[0], nil
}

func (s PostgreSQLSource) Workouts(ctx context.Context, accountID uuid.UUID, limit int) ([]Workout, error) {
	rows, err := s.Pool.Query(ctx, `WITH candidates AS (
		SELECT workout.id,workout_type.type_key,workout_type.provider_label,workout.started_at,
			CASE
				WHEN lower(workout_type.type_key||' '||workout_type.provider_label) ~ '(cycl|bicycl|bike|biking)' THEN 'bicycle'
				WHEN lower(workout_type.type_key||' '||workout_type.provider_label) ~ '(walk|run|hik|foot|climb|trek)' THEN 'foot'
				ELSE 'shared_public'
			END AS movement_mode
		FROM app.workouts workout
		JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
		JOIN app.workout_types workout_type ON workout_type.account_id=workout.account_id AND workout_type.id=workout.workout_type_id
		WHERE workout.account_id=$1 AND workout.deletion_requested_at IS NULL
	), ranked AS (
		SELECT *,row_number() OVER (PARTITION BY movement_mode ORDER BY started_at DESC,id) AS mode_rank
		FROM candidates
	)
	SELECT id,type_key,provider_label FROM ranked
	ORDER BY mode_rank,movement_mode,started_at DESC,id LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("query workouts")
	}
	defer rows.Close()
	result := make([]Workout, 0, limit)
	for rows.Next() {
		var workout Workout
		if err := rows.Scan(&workout.ID, &workout.TypeKey, &workout.ProviderLabel); err != nil {
			return nil, fmt.Errorf("scan workout")
		}
		result = append(result, workout)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read workouts")
	}
	return result, nil
}

func (s PostgreSQLSource) Points(ctx context.Context, accountID, workoutID uuid.UUID) ([]coverage.GeographicObservation, error) {
	rows, err := s.Pool.Query(ctx, `SELECT sequence,longitude,latitude,recorded_at,
		COALESCE(horizontal_accuracy,0),course,course_accuracy
		FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2 ORDER BY sequence`, accountID, workoutID)
	if err != nil {
		return nil, fmt.Errorf("query route points")
	}
	defer rows.Close()
	var result []coverage.GeographicObservation
	for rows.Next() {
		var point coverage.GeographicObservation
		var recordedAt *time.Time
		if err := rows.Scan(&point.Sequence, &point.Longitude, &point.Latitude, &recordedAt,
			&point.AccuracyMeters, &point.HeadingDegrees, &point.HeadingAccuracy); err != nil {
			return nil, fmt.Errorf("scan route point")
		}
		if recordedAt != nil {
			point.Time = *recordedAt
		}
		result = append(result, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read route points")
	}
	return result, nil
}

type OSMSnapshotFactory struct{ Pool *pgxpool.Pool }

func (f OSMSnapshotFactory) Begin(ctx context.Context) (Snapshot, error) {
	snapshot, err := osm.BeginMatcherSnapshot(ctx, f.Pool)
	if err != nil {
		return nil, fmt.Errorf("begin snapshot")
	}
	return snapshot, nil
}
