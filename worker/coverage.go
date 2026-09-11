package worker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"time"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type coverageRoutePoint struct {
	Sequence                                                            int
	Timestamp                                                           *time.Time
	Latitude, Longitude                                                 float64
	Altitude, Speed, Course                                             *float64
	HorizontalAccuracy, VerticalAccuracy, SpeedAccuracy, CourseAccuracy *float64
}

func coveragePoints(points []healthautoexport.RoutePoint) []coverageRoutePoint {
	result := make([]coverageRoutePoint, len(points))
	for i := range points {
		point := &points[i]
		timestamp := point.Timestamp
		result[i] = coverageRoutePoint{point.Sequence, &timestamp, point.Latitude, point.Longitude,
			point.Altitude, point.Speed, point.Course, point.HorizontalAccuracy, point.VerticalAccuracy, point.SpeedAccuracy, point.CourseAccuracy}
	}
	return result
}

func canonicalRouteInputSHA256(points []coverageRoutePoint) [32]byte {
	digest := sha256.New()
	writeUint64(digest, uint64(len(points)))
	for _, point := range points {
		writeUint64(digest, uint64(int64(point.Sequence)))
		writeTime(digest, point.Timestamp)
		writeFloat(digest, point.Latitude)
		writeFloat(digest, point.Longitude)
		for _, value := range []*float64{point.Altitude, point.Speed, point.Course, point.HorizontalAccuracy, point.VerticalAccuracy, point.SpeedAccuracy, point.CourseAccuracy} {
			writeOptionalFloat(digest, value)
		}
	}
	var result [32]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}
func writeFloat(digest hash.Hash, value float64) { writeUint64(digest, math.Float64bits(value)) }
func writeOptionalFloat(digest hash.Hash, value *float64) {
	if value == nil {
		_, _ = digest.Write([]byte{0})
		return
	}
	_, _ = digest.Write([]byte{1})
	writeFloat(digest, *value)
}
func writeTime(digest hash.Hash, value *time.Time) {
	if value == nil {
		_, _ = digest.Write([]byte{0})
		return
	}
	_, _ = digest.Write([]byte{1})
	text := value.UTC().Format(time.RFC3339Nano)
	writeUint64(digest, uint64(len(text)))
	_, _ = digest.Write([]byte(text))
}

func (r *Runner) updateCoverageReadiness(ctx context.Context, tx pgx.Tx, job claimedJob, workoutID uuid.UUID, points []coverageRoutePoint) error {
	digest := canonicalRouteInputSHA256(points)
	var revision int64
	var changed bool
	if err := tx.QueryRow(ctx, `SELECT route_input_revision,digest_changed FROM app.initialize_workout_coverage($1,$2,$3,$4,$5,$6)`,
		job.accountID, workoutID, digest[:], job.id, r.workerID, job.lease).Scan(&revision, &changed); err != nil {
		return fmt.Errorf("initialize workout coverage: %w", err)
	}
	if r.osmDB == nil {
		return nil
	}
	coordinates := make([]osm.RouteCoordinate, len(points))
	for i, point := range points {
		coordinates[i] = osm.RouteCoordinate{Longitude: point.Longitude, Latitude: point.Latitude}
	}
	readiness, err := osm.ResolveRouteRegions(ctx, r.osmDB, coordinates)
	if err != nil {
		return err
	}
	regions, err := json.Marshal(readiness.Regions)
	if err != nil {
		return fmt.Errorf("encode workout coverage regions: %w", err)
	}
	var persisted bool
	reason := any(nil)
	if readiness.Reason != "" {
		reason = readiness.Reason
	}
	if err := tx.QueryRow(ctx, `SELECT app.set_workout_coverage_readiness($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		job.accountID, workoutID, revision, readiness.State, reason, regions, job.id, r.workerID, job.lease).Scan(&persisted); err != nil {
		return fmt.Errorf("persist workout coverage readiness: %w", err)
	}
	_ = changed
	return nil
}

func (r *Runner) repairCoverageReadiness(ctx context.Context, job claimedJob) error {
	if r.osmDB == nil {
		return nil
	}
	listTx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return err
	}
	defer listTx.Rollback(ctx)
	activeGenerations, err := osm.ActiveRegionGenerations(ctx, r.osmDB)
	if err != nil {
		return err
	}
	rows, err := listTx.Query(ctx, `SELECT route.workout_id,state.readiness_state,
		COALESCE(jsonb_object_agg(region.region_id,region.desired_osm_generation)
			FILTER (WHERE region.region_id IS NOT NULL),'{}'::jsonb)
		FROM app.workout_routes route
		JOIN app.workouts workout ON workout.id=route.workout_id AND workout.account_id=route.account_id
		LEFT JOIN app.workout_coverage_states state ON state.account_id=route.account_id AND state.workout_id=route.workout_id
		LEFT JOIN app.workout_coverage_regions region ON region.account_id=route.account_id AND region.workout_id=route.workout_id
		WHERE route.account_id=$1 AND workout.deletion_requested_at IS NULL
		GROUP BY route.workout_id,state.readiness_state ORDER BY route.workout_id`, job.accountID)
	if err != nil {
		return err
	}
	var workoutIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var state *string
		var encodedRegions []byte
		if err := rows.Scan(&id, &state, &encodedRegions); err != nil {
			rows.Close()
			return err
		}
		regions := make(map[string]int64)
		if err := json.Unmarshal(encodedRegions, &regions); err != nil {
			rows.Close()
			return fmt.Errorf("decode workout coverage regions: %w", err)
		}
		if state != nil && *state == "map_data_ready" && coverageGenerationsCurrent(regions, activeGenerations) {
			continue
		}
		workoutIDs = append(workoutIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := listTx.Commit(ctx); err != nil {
		return err
	}
	for _, workoutID := range workoutIDs {
		tx, err := beginAccount(ctx, r.db, job.accountID)
		if err != nil {
			return err
		}
		pointRows, err := tx.Query(ctx, `SELECT sequence,recorded_at,latitude,longitude,altitude,speed,course,
			horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy FROM app.workout_route_points
			WHERE account_id=$1 AND workout_id=$2 ORDER BY sequence`, job.accountID, workoutID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		var points []coverageRoutePoint
		for pointRows.Next() {
			var point coverageRoutePoint
			if err := pointRows.Scan(&point.Sequence, &point.Timestamp, &point.Latitude, &point.Longitude,
				&point.Altitude, &point.Speed, &point.Course, &point.HorizontalAccuracy, &point.VerticalAccuracy, &point.SpeedAccuracy, &point.CourseAccuracy); err != nil {
				pointRows.Close()
				_ = tx.Rollback(ctx)
				return err
			}
			points = append(points, point)
		}
		pointRows.Close()
		if err := pointRows.Err(); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := r.updateCoverageReadiness(ctx, tx, job, workoutID, points); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func coverageGenerationsCurrent(regions, active map[string]int64) bool {
	if len(regions) == 0 {
		return false
	}
	for regionID, generation := range regions {
		if active[regionID] != generation {
			return false
		}
	}
	return true
}
