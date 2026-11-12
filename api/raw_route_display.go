package api

import (
	"context"
	"fmt"
	"math"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func queryDisplayedRoutePoints(ctx context.Context, tx pgx.Tx, workout, account uuid.UUID) ([]generated.NormalizedRoutePoint, error) {
	rows, err := tx.Query(ctx, `SELECT p.sequence,d.component,p.recorded_at,p.timestamp_offset_minutes,p.longitude,p.latitude,p.altitude,p.speed,p.course,
 p.horizontal_accuracy,p.vertical_accuracy,p.speed_accuracy,p.course_accuracy
 FROM app.raw_route_display_points($2,$1) d JOIN app.workout_route_points p
 ON p.account_id=$2 AND p.workout_id=$1 AND p.sequence=d.sequence ORDER BY p.sequence`, workout, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]generated.NormalizedRoutePoint, 0)
	for rows.Next() {
		var p generated.NormalizedRoutePoint
		var offset *int
		var altitude, speed, course, horizontal, vertical, speedAccuracy, courseAccuracy *float64
		if err = rows.Scan(&p.Sequence, &p.Component, &p.RecordedAt, &offset, &p.Longitude, &p.Latitude, &altitude, &speed, &course, &horizontal, &vertical, &speedAccuracy, &courseAccuracy); err != nil {
			return nil, err
		}
		setNullable(&p.OriginalOffsetMinutes, offset)
		setNullable(&p.AltitudeMeters, altitude)
		setNullable(&p.SpeedMetersPerSecond, speed)
		setNullable(&p.CourseDegrees, course)
		setNullable(&p.HorizontalAccuracyMeters, horizontal)
		setNullable(&p.VerticalAccuracyMeters, vertical)
		setNullable(&p.SpeedAccuracyMetersPerSecond, speedAccuracy)
		setNullable(&p.CourseAccuracyDegrees, courseAccuracy)
		points = append(points, p)
		if len(points) > 250000 {
			return nil, fmt.Errorf("display route exceeds export bound")
		}
	}
	return points, rows.Err()
}

func populateDisplayedGeoJSON(result *generated.WorkoutGeoJSONFeature, points []generated.NormalizedRoutePoint) error {
	completeAltitude := true
	for _, p := range points {
		if _, err := p.AltitudeMeters.Get(); err != nil {
			completeAltitude = false
		}
	}
	bounds := generated.RouteBounds{MinimumLongitude: math.Inf(1), MinimumLatitude: math.Inf(1), MaximumLongitude: math.Inf(-1), MaximumLatitude: math.Inf(-1)}
	elevation := generated.RouteElevation{MinimumMeters: math.Inf(1), MaximumMeters: math.Inf(-1)}
	var parts [][][]float64
	priorComponent := 0
	priorAltitude := 0.0
	for _, p := range points {
		coordinate := []float64{p.Longitude, p.Latitude}
		if completeAltitude {
			altitude, _ := p.AltitudeMeters.Get()
			coordinate = append(coordinate, altitude)
			elevation.MinimumMeters = math.Min(elevation.MinimumMeters, altitude)
			elevation.MaximumMeters = math.Max(elevation.MaximumMeters, altitude)
			if p.Component == priorComponent && altitude > priorAltitude {
				elevation.GainMeters += altitude - priorAltitude
			}
			priorAltitude = altitude
		}
		if p.Component != priorComponent {
			parts = append(parts, nil)
		}
		parts[len(parts)-1] = append(parts[len(parts)-1], coordinate)
		priorComponent = p.Component
		bounds.MinimumLongitude = math.Min(bounds.MinimumLongitude, p.Longitude)
		bounds.MaximumLongitude = math.Max(bounds.MaximumLongitude, p.Longitude)
		bounds.MinimumLatitude = math.Min(bounds.MinimumLatitude, p.Latitude)
		bounds.MaximumLatitude = math.Max(bounds.MaximumLatitude, p.Latitude)
	}
	result.Properties.PointCount = len(points)
	result.Properties.Bounds = bounds
	if completeAltitude {
		result.Properties.ElevationSummary.Set(elevation)
	} else {
		result.Properties.ElevationSummary.SetNull()
	}
	if len(parts) == 1 {
		return result.Geometry.FromGeoJSONLineString(generated.GeoJSONLineString{Type: generated.LineString, Coordinates: parts[0]})
	}
	return result.Geometry.FromGeoJSONMultiLineString(generated.GeoJSONMultiLineString{Type: generated.MultiLineString, Coordinates: parts})
}
