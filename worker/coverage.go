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

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

type coverageRoutePoint struct {
	Sequence           int        `json:"sequence"`
	Timestamp          *time.Time `json:"recorded_at"`
	Latitude           float64    `json:"latitude"`
	Longitude          float64    `json:"longitude"`
	Altitude           *float64   `json:"altitude"`
	Speed              *float64   `json:"speed"`
	Course             *float64   `json:"course"`
	HorizontalAccuracy *float64   `json:"horizontal_accuracy"`
	VerticalAccuracy   *float64   `json:"vertical_accuracy"`
	SpeedAccuracy      *float64   `json:"speed_accuracy"`
	CourseAccuracy     *float64   `json:"course_accuracy"`
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

func routeReadinessForAutoAddPolicy(readiness osm.RouteRegionReadiness, autoAddRegions bool) osm.RouteRegionReadiness {
	if readiness.State == "pending" && !autoAddRegions {
		return osm.RouteRegionReadiness{State: "unavailable", Reason: "no_provider_region", Regions: []osm.RegionGeneration{}}
	}
	return readiness
}

func nullableReadinessReason(reason string) any {
	if reason == "" {
		return nil
	}
	return reason
}

func (r *Runner) repairCoverageReadiness(ctx context.Context, job claimedJob) error {
	if r.osmDB == nil {
		return nil
	}
	if err := r.drainIngestCoverage(ctx, job, false); err != nil {
		return err
	}
	activeGenerations, err := osm.ActiveRegionGenerations(ctx, r.osmDB)
	if err != nil {
		return database.RedactOSMConnectionError(err)
	}
	var cursor *uuid.UUID
	for {
		workoutIDs, next, err := func() ([]uuid.UUID, *uuid.UUID, error) {
			listTx, err := beginAccount(ctx, r.db, job.accountID)
			if err != nil {
				return nil, nil, err
			}
			defer listTx.Rollback(ctx)
			rows, err := listTx.Query(ctx, `WITH pending_adoptions AS MATERIALIZED (
		SELECT workout_id FROM app.read_pending_adopted_workout_coverage_refreshes($1,$2,$3,$4)
		UNION SELECT workout_id FROM app.read_pending_ingest_coverage_workouts($1,$2,$3,$4)
	) SELECT route.workout_id,state.readiness_state,
		COALESCE(jsonb_object_agg(region.region_id,region.desired_osm_generation)
			FILTER (WHERE region.region_id IS NOT NULL),'{}'::jsonb)
		FROM app.workout_routes route
		JOIN app.workouts workout ON workout.id=route.workout_id AND workout.account_id=route.account_id
		LEFT JOIN app.workout_coverage_states state ON state.account_id=route.account_id AND state.workout_id=route.workout_id
		LEFT JOIN app.workout_coverage_regions region ON region.account_id=route.account_id AND region.workout_id=route.workout_id
		WHERE route.account_id=$1 AND workout.deletion_requested_at IS NULL
		AND workout.source_id=app.read_ingest_coverage_source_id($1,$2,$3,$4)
		AND ($5::uuid IS NULL OR route.workout_id>$5)
		AND NOT EXISTS (SELECT 1 FROM pending_adoptions pending
			WHERE pending.workout_id=route.workout_id)
		GROUP BY route.workout_id,state.readiness_state ORDER BY route.workout_id LIMIT 25`, job.accountID, job.id, r.workerID, job.lease, cursor)
			if err != nil {
				return nil, nil, err
			}
			var workoutIDs []uuid.UUID
			var next *uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				var state *string
				var encodedRegions []byte
				if err := rows.Scan(&id, &state, &encodedRegions); err != nil {
					rows.Close()
					return nil, nil, err
				}
				next = &id
				regions := make(map[string]int64)
				if err := json.Unmarshal(encodedRegions, &regions); err != nil {
					rows.Close()
					return nil, nil, fmt.Errorf("decode workout coverage regions: %w", err)
				}
				if state != nil && *state == "map_data_ready" && coverageGenerationsCurrent(regions, activeGenerations) {
					continue
				}
				workoutIDs = append(workoutIDs, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, nil, err
			}
			return workoutIDs, next, listTx.Commit(ctx)
		}()
		if err != nil {
			return coverageHandoffError(err)
		}
		if next == nil {
			return nil
		}
		for _, workoutID := range workoutIDs {
			if err := r.repairWorkoutCoverageReadiness(ctx, job, workoutID); err != nil {
				return err
			}
		}
		if err := r.drainIngestCoverage(ctx, job, false); err != nil {
			return err
		}
		cursor = next
	}
}

func (r *Runner) repairWorkoutCoverageReadiness(ctx context.Context, job claimedJob, workoutID uuid.UUID) error {
	for retries := 0; ; retries++ {
		err := func() error {
			tx, _, err := r.beginIngestWrite(ctx, job)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			// Recording is fenced, metadata-only, and cannot create a Coverage parent.
			// Common intake takes the fresh full route snapshot after this commit.
			var recorded bool
			if err := tx.QueryRow(ctx, `SELECT app.record_ingest_coverage_repair($1,$2,$3,$4,$5,$6)`,
				job.accountID, job.id, r.workerID, job.lease, workoutID, database.SupportedSchemaVersion).Scan(&recorded); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}()
		if err == nil {
			return nil
		}
		if retryErr := waitCoverageHandoffRetry(ctx, err, retries); retryErr != nil {
			return coverageHandoffError(retryErr)
		}
	}
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
