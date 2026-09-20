package routepipeline

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
)

type ProductionSnapshot interface {
	Snapshot
	CopySegments(context.Context, []osm.MatcherSegmentRef) ([]osm.MatcherSegmentCopy, error)
}

type PreparedMatch struct {
	Segment          osm.MatcherSegmentCopy
	CoveredGeoJSON   json.RawMessage
	CoveredMeters    float64
	FirstTraversedAt time.Time
	FirstRouteOrder  int
}

type segmentKey struct {
	id         [16]byte
	region     string
	generation int64
}

type sourceInterval struct {
	from, to         float64
	routeOrder       int
	firstTraversedAt time.Time
}

func ProductionLimits() Limits { return DiagnosticLimits() }

func Prepare(ctx context.Context, snapshot ProductionSnapshot, result Result, fallbackTime time.Time) ([]PreparedMatch, error) {
	groups := make(map[segmentKey][]sourceInterval)
	for _, evidence := range result.Evidence {
		key := segmentKey{id: evidence.Portion.PhysicalSegmentID, region: evidence.Portion.RegionID, generation: evidence.Portion.GenerationID}
		groups[key] = append(groups[key], sourceInterval{from: evidence.Portion.SourceFromFraction,
			to: evidence.Portion.SourceToFraction, routeOrder: evidence.RouteOrder, firstTraversedAt: evidence.FirstTraversedAt})
	}
	if len(groups) == 0 {
		return nil, nil
	}
	keys := make([]segmentKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].region != keys[j].region {
			return keys[i].region < keys[j].region
		}
		if keys[i].generation != keys[j].generation {
			return keys[i].generation < keys[j].generation
		}
		return string(keys[i].id[:]) < string(keys[j].id[:])
	})

	refs := make([]osm.MatcherPortionRef, 0, len(result.Evidence))
	intervalCounts := make([]int, len(keys))
	firstTimes := make([]time.Time, len(keys))
	firstOrders := make([]int, len(keys))
	for i, key := range keys {
		intervals := groups[key]
		earliestOrder := int(^uint(0) >> 1)
		for _, interval := range intervals {
			if interval.routeOrder < earliestOrder {
				earliestOrder = interval.routeOrder
				firstTimes[i] = interval.firstTraversedAt
			}
		}
		sort.Slice(intervals, func(i, j int) bool {
			if intervals[i].from != intervals[j].from {
				return intervals[i].from < intervals[j].from
			}
			return intervals[i].to < intervals[j].to
		})
		merged := intervals[:0]
		for _, interval := range intervals {
			if len(merged) == 0 || interval.from > merged[len(merged)-1].to+1e-9 {
				merged = append(merged, interval)
				continue
			}
			if interval.to > merged[len(merged)-1].to {
				merged[len(merged)-1].to = interval.to
			}
		}
		if firstTimes[i].IsZero() {
			firstTimes[i] = fallbackTime
		}
		firstOrders[i] = earliestOrder
		intervalCounts[i] = len(merged)
		for _, interval := range merged {
			refs = append(refs, osm.MatcherPortionRef{SegmentID: key.id, RegionID: key.region, GenerationID: key.generation,
				Direction: osm.MatcherForward, SourceFromFraction: interval.from, SourceToFraction: interval.to})
		}
	}
	if len(refs) > DiagnosticLimits().Portions {
		return nil, &LimitError{Field: "dissolved portions", Limit: DiagnosticLimits().Portions}
	}
	clips := make([]osm.MatcherClippedPortion, 0, len(refs))
	for start := 0; start < len(refs); start += DiagnosticLimits().Segments {
		end := min(start+DiagnosticLimits().Segments, len(refs))
		batch, err := snapshot.ClipPortions(ctx, refs[start:end])
		if err != nil {
			return nil, err
		}
		if len(batch) != end-start {
			return nil, errors.New("OSM dissolved clip response is incomplete")
		}
		for i, clip := range batch {
			ref := refs[start+i]
			if clip.Ordinal != i || clip.SegmentID != ref.SegmentID || clip.RegionID != ref.RegionID || clip.GenerationID != ref.GenerationID {
				return nil, errors.New("OSM dissolved clip response is invalid")
			}
		}
		clips = append(clips, batch...)
	}
	copyRefs := make([]osm.MatcherSegmentRef, len(keys))
	for i, key := range keys {
		copyRefs[i] = osm.MatcherSegmentRef{SegmentID: key.id, RegionID: key.region, GenerationID: key.generation}
	}
	segmentCopies, err := snapshot.CopySegments(ctx, copyRefs)
	if err != nil {
		return nil, err
	}
	if len(segmentCopies) != len(keys) {
		return nil, errors.New("OSM segment copy response is incomplete")
	}
	for i, item := range segmentCopies {
		key := keys[i]
		if item.Ordinal != i || item.SegmentID != key.id || item.RegionID != key.region || item.GenerationID != key.generation {
			return nil, errors.New("OSM segment copy response is invalid")
		}
	}
	prepared := make([]PreparedMatch, len(keys))
	clipIndex := 0
	for i := range keys {
		coordinates := make([][][]float64, 0, intervalCounts[i])
		coveredMeters := 0.0
		for range intervalCounts[i] {
			var line struct {
				Type        string      `json:"type"`
				Coordinates [][]float64 `json:"coordinates"`
			}
			if json.Unmarshal(clips[clipIndex].GeoJSON, &line) != nil || line.Type != "LineString" || len(line.Coordinates) < 2 {
				return nil, errors.New("OSM dissolved clip geometry is invalid")
			}
			coordinates = append(coordinates, line.Coordinates)
			coveredMeters += clips[clipIndex].LengthMeters
			clipIndex++
		}
		geometry, err := json.Marshal(struct {
			Type        string        `json:"type"`
			Coordinates [][][]float64 `json:"coordinates"`
		}{Type: "MultiLineString", Coordinates: coordinates})
		if err != nil {
			return nil, err
		}
		prepared[i] = PreparedMatch{Segment: segmentCopies[i], CoveredGeoJSON: geometry,
			CoveredMeters: coveredMeters, FirstTraversedAt: firstTimes[i], FirstRouteOrder: firstOrders[i]}
	}
	return prepared, nil
}
