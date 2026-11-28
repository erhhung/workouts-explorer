package coverage

import (
	"context"
	"encoding/json"
	"math"
)

// ServiceTraversalSupported verifies the selected geometry against directed raw
// movement. Crossing an entrance, standing nearby or a graph-only shortcut is not
// evidence for travelling down an aisle. Callers supply one hard-gap-bounded leg.
func ServiceTraversalSupported(ctx context.Context, geometry json.RawMessage, observations []GeographicObservation, rules Rules, corridor float64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var line struct {
		Type        string
		Coordinates [][]float64
	}
	if json.Unmarshal(geometry, &line) != nil || line.Type != "LineString" || len(line.Coordinates) < 2 || len(observations) < 2 {
		return false, nil
	}
	if len(line.Coordinates) > 4096 || len(observations) > 512 {
		return false, &OSMEvaluationLimitError{Field: "service support", Limit: 4096}
	}
	origin := observations[0]
	type rawLeg struct {
		from, to       Point
		dx, dy, length float64
	}
	legs := make([]rawLeg, 0, len(observations)-1)
	for i := 1; i < len(observations); i++ {
		if GeographicGapReason(observations[i-1], observations[i], rules) != "" {
			return false, nil
		}
		a, b := localPoint(origin, observations[i-1].Longitude, observations[i-1].Latitude), localPoint(origin, observations[i].Longitude, observations[i].Latitude)
		dx, dy := b.X-a.X, b.Y-a.Y
		if length := math.Hypot(dx, dy); length >= 1 {
			legs = append(legs, rawLeg{a, b, dx, dy, length})
		}
	}
	if len(legs) == 0 {
		return false, nil
	}
	checks := 0
	samples := 0
	for i := 1; i < len(line.Coordinates); i++ {
		if len(line.Coordinates[i-1]) < 2 || len(line.Coordinates[i]) < 2 {
			return false, nil
		}
		a, b := localPoint(origin, line.Coordinates[i-1][0], line.Coordinates[i-1][1]), localPoint(origin, line.Coordinates[i][0], line.Coordinates[i][1])
		dx, dy := b.X-a.X, b.Y-a.Y
		length := math.Hypot(dx, dy)
		if length < 1e-6 {
			continue
		}
		steps := max(1, int(math.Ceil(length/5)))
		samples += steps + 1
		if samples > 4096 {
			return false, &OSMEvaluationLimitError{Field: "service support samples", Limit: 4096}
		}
		for step := 0; step <= steps; step++ {
			point := Point{a.X + dx*float64(step)/float64(steps), a.Y + dy*float64(step)/float64(steps)}
			supported := false
			for _, leg := range legs {
				checks++
				if checks&127 == 0 {
					if err := ctx.Err(); err != nil {
						return false, err
					}
				}
				if math.Abs((dx*leg.dx+dy*leg.dy)/(length*leg.length)) < math.Cos(35*math.Pi/180) {
					continue
				}
				fraction := math.Max(0, math.Min(1, ((point.X-leg.from.X)*leg.dx+(point.Y-leg.from.Y)*leg.dy)/(leg.length*leg.length)))
				if math.Hypot(point.X-leg.from.X-fraction*leg.dx, point.Y-leg.from.Y-fraction*leg.dy) <= corridor {
					supported = true
					break
				}
			}
			if !supported {
				return false, nil
			}
		}
	}
	return true, nil
}
