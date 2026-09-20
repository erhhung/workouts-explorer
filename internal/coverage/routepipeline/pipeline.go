// Package routepipeline evaluates one complete route against an OSM snapshot.
package routepipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

const WindowSize = 128

type Snapshot interface {
	Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error)
	IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error)
	ClipPortions(context.Context, []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error)
	Generations(context.Context) ([]osm.MatcherGeneration, error)
	UnavailableRegions(context.Context, []osm.MatcherObservation) ([]osm.MatcherUnavailableRegion, error)
}

type Limits struct {
	SampledPoints      int
	Portions           int
	Segments           int
	Generations        int
	UnavailableRegions int
}

func DiagnosticLimits() Limits {
	return Limits{SampledPoints: 10000, Portions: 10000, Segments: 4096, Generations: 256, UnavailableRegions: 64}
}

type Config struct {
	Rules        coverage.Rules
	Sampling     coverage.SamplingRules
	RoadGeometry coverage.RoadGeometryRules
	MovementMode coverage.MovementMode
	Limits       Limits
}

type Counts struct {
	OriginalPoints, SampledPoints                        int
	MatchedPoints, AmbiguousPoints, UnmatchedPoints      int
	RejectedPoints, Traversals, Portions, UniqueSegments int
}

type Evidence struct {
	Portion          coverage.TraversedPortion
	Class            string
	Clipped          osm.MatcherClippedPortion
	RouteOrder       int
	FirstTraversedAt time.Time
}

type Stats struct {
	Windows, Candidates, Suppressed, GraphNodes, GraphEdges, InvalidClips int
	Splits                                                                map[coverage.SplitReason]int
}

type Result struct {
	Counts             Counts
	Evidence           []Evidence
	Generations        []osm.MatcherGeneration
	UnavailableRegions []osm.MatcherUnavailableRegion
	Stats              Stats
}

type LimitError struct {
	Field string
	Limit int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("coverage route %s exceeds its bound of %d", e.Field, e.Limit)
}

func MovementMode(typeKey, providerLabel string) coverage.MovementMode {
	value := strings.ToLower(typeKey + " " + providerLabel)
	for _, token := range []string{"cycl", "bicycl", "bike", "biking"} {
		if strings.Contains(value, token) {
			return coverage.MovementBicycle
		}
	}
	for _, token := range []string{"walk", "run", "hik", "foot", "climb", "trek"} {
		if strings.Contains(value, token) {
			return coverage.MovementFoot
		}
	}
	return coverage.MovementSharedPublic
}

type matchedPortion struct {
	portion          coverage.TraversedPortion
	class            string
	window           int
	routeOrder       int
	firstTraversedAt time.Time
}

func Evaluate(ctx context.Context, snapshot Snapshot, observations []coverage.GeographicObservation, cfg Config) (Result, error) {
	points := coverage.SampleGeographicObservations(observations, cfg.Sampling, cfg.Rules)
	if len(points) > cfg.Limits.SampledPoints {
		return Result{}, &LimitError{Field: "sampled points", Limit: cfg.Limits.SampledPoints}
	}
	allGenerations, err := snapshot.Generations(ctx)
	if err != nil {
		return Result{}, err
	}
	result := Result{Counts: Counts{OriginalPoints: len(observations), SampledPoints: len(points)}, Stats: Stats{Splits: make(map[coverage.SplitReason]int)}}
	options := coverage.DefaultOSMEvaluationOptions()
	options.RoadGeometry = cfg.RoadGeometry
	options.MaxCandidatesPerObservation = 16
	options.MovementMode = cfg.MovementMode
	options.Sampling.MinimumDistanceMeters = 0
	var portions []matchedPortion
	var windowStarts []int
	windowOrdinal := 0
	for start := 0; start < len(points); {
		windowStarts = append(windowStarts, start)
		end := min(start+WindowSize, len(points))
		matched, stats, err := coverage.MatchOSM(ctx, snapshot, points[start:end], cfg.Rules, options)
		if err != nil {
			return Result{}, err
		}
		result.addStats(stats, matched.Splits)
		skip := 0
		if start > 0 {
			skip = 1
		}
		for i := skip; i < len(matched.Observations); i++ {
			result.addObservation(matched.Observations[i].Status)
		}
		result.Counts.Traversals += len(matched.Traversals)
		for _, traversal := range matched.Traversals {
			class := traversalClass(traversal)
			for portionIndex, portion := range traversal.Portions {
				relativeOrder := traversal.FirstObservation
				if len(traversal.Portions) > 1 {
					relativeOrder += portionIndex * max(0, traversal.LastObservation-traversal.FirstObservation) / (len(traversal.Portions) - 1)
				}
				routeOrder := start + relativeOrder
				var firstTraversedAt time.Time
				if routeOrder >= 0 && routeOrder < len(points) {
					firstTraversedAt = points[routeOrder].Time
				}
				portions = append(portions, matchedPortion{portion: portion, class: class, window: windowOrdinal,
					routeOrder: routeOrder, firstTraversedAt: firstTraversedAt})
			}
		}
		if end == len(points) {
			break
		}
		start = end - 1
		windowOrdinal++
	}
	portions, stats, err := completeCrossWindowConnectedRoadTurns(ctx, snapshot, points, windowStarts, portions, cfg.Rules, options)
	if err != nil {
		return Result{}, err
	}
	result.addSupplementalStats(stats)
	portions, stats, err = replaceCrossWindowRoadTangents(ctx, snapshot, points, windowStarts, portions, cfg.Rules, options)
	if err != nil {
		return Result{}, err
	}
	result.addSupplementalStats(stats)
	if len(portions) > cfg.Limits.Portions {
		return Result{}, &LimitError{Field: "portions", Limit: cfg.Limits.Portions}
	}

	segments := make(map[uuid.UUID]struct{})
	uniqueRefs := make([]osm.MatcherPortionRef, 0, len(portions))
	refIndexes := make(map[osm.MatcherPortionRef]int, len(portions))
	portionRefs := make([]int, len(portions))
	for i, item := range portions {
		segments[item.portion.PhysicalSegmentID] = struct{}{}
		ref := osm.MatcherPortionRef{SegmentID: item.portion.PhysicalSegmentID, RegionID: item.portion.RegionID,
			GenerationID: item.portion.GenerationID, Direction: osm.MatcherDirection(item.portion.Direction),
			SourceFromFraction: item.portion.SourceFromFraction, SourceToFraction: item.portion.SourceToFraction}
		index, exists := refIndexes[ref]
		if !exists {
			index = len(uniqueRefs)
			refIndexes[ref] = index
			uniqueRefs = append(uniqueRefs, ref)
		}
		portionRefs[i] = index
	}
	if len(segments) > cfg.Limits.Segments {
		return Result{}, &LimitError{Field: "segments", Limit: cfg.Limits.Segments}
	}
	clipped := make([]osm.MatcherClippedPortion, len(uniqueRefs))
	for start := 0; start < len(uniqueRefs); start += cfg.Limits.Segments {
		end := min(start+cfg.Limits.Segments, len(uniqueRefs))
		batch, err := snapshot.ClipPortions(ctx, uniqueRefs[start:end])
		if err != nil {
			return Result{}, err
		}
		for _, item := range batch {
			item.Ordinal += start
			if item.Ordinal < start || item.Ordinal >= end {
				return Result{}, errors.New("OSM clipped portion response is invalid")
			}
			clipped[item.Ordinal] = item
		}
	}
	usedGenerations := make(map[string]struct{})
	clear(segments)
	for i, item := range portions {
		clip := clipped[portionRefs[i]]
		if !ValidClip(clip) {
			result.Stats.InvalidClips++
			continue
		}
		result.Evidence = append(result.Evidence, Evidence{Portion: item.portion, Class: item.class, Clipped: clip,
			RouteOrder: item.routeOrder, FirstTraversedAt: item.firstTraversedAt})
		segments[clip.SegmentID] = struct{}{}
		usedGenerations[generationKey(clip.RegionID, clip.GenerationID)] = struct{}{}
	}
	for _, generation := range allGenerations {
		if _, used := usedGenerations[generationKey(generation.RegionID, generation.GenerationID)]; used {
			result.Generations = append(result.Generations, generation)
		}
	}
	if len(result.Generations) != len(usedGenerations) {
		return Result{}, errors.New("OSM generation provenance unavailable")
	}
	if len(result.Generations) > cfg.Limits.Generations {
		return Result{}, &LimitError{Field: "generations", Limit: cfg.Limits.Generations}
	}
	if len(result.Evidence) == 0 {
		regionPoints := points
		if len(regionPoints) > 256 {
			sampled := make([]coverage.GeographicObservation, 256)
			for i := range sampled {
				sampled[i] = regionPoints[i*(len(regionPoints)-1)/(len(sampled)-1)]
			}
			regionPoints = sampled
		}
		matcherPoints := make([]osm.MatcherObservation, len(regionPoints))
		for i, point := range regionPoints {
			matcherPoints[i] = osm.MatcherObservation{Longitude: point.Longitude, Latitude: point.Latitude}
		}
		result.UnavailableRegions, err = snapshot.UnavailableRegions(ctx, matcherPoints)
		if err != nil {
			return Result{}, err
		}
		if len(result.UnavailableRegions) > cfg.Limits.UnavailableRegions {
			return Result{}, &LimitError{Field: "unavailable regions", Limit: cfg.Limits.UnavailableRegions}
		}
	}
	result.Counts.Portions, result.Counts.UniqueSegments = len(result.Evidence), len(segments)
	return result, nil
}

func generationKey(region string, generation int64) string {
	return fmt.Sprintf("%s\x00%d", region, generation)
}

func (r *Result) addObservation(status coverage.ObservationStatus) {
	switch status {
	case coverage.ObservationMatched:
		r.Counts.MatchedPoints++
	case coverage.ObservationAmbiguous:
		r.Counts.AmbiguousPoints++
	case coverage.ObservationRejected:
		r.Counts.RejectedPoints++
	default:
		r.Counts.UnmatchedPoints++
	}
}

func (r *Result) addStats(stats coverage.OSMEvaluationStats, splits []coverage.Split) {
	r.Stats.Windows++
	r.Stats.Candidates += stats.CandidateCount
	r.Stats.Suppressed += stats.SuppressedPedestrianCandidates
	r.Stats.GraphNodes += stats.GraphNodeCount
	r.Stats.GraphEdges += stats.GraphEdgeCount
	for _, split := range splits {
		r.Stats.Splits[split.Reason]++
	}
}

type supplementalStats struct{ windows, candidates, suppressed, graphNodes, graphEdges int }

func (r *Result) addSupplementalStats(stats supplementalStats) {
	r.Stats.Windows += stats.windows
	r.Stats.Candidates += stats.candidates
	r.Stats.Suppressed += stats.suppressed
	r.Stats.GraphNodes += stats.graphNodes
	r.Stats.GraphEdges += stats.graphEdges
}

func ValidClip(clip osm.MatcherClippedPortion) bool {
	if clip.LengthMeters <= 0 || math.IsNaN(clip.LengthMeters) || math.IsInf(clip.LengthMeters, 0) {
		return false
	}
	var geometry struct {
		Type        string      `json:"type"`
		Coordinates [][]float64 `json:"coordinates"`
	}
	if json.Unmarshal(clip.GeoJSON, &geometry) != nil || geometry.Type != "LineString" || len(geometry.Coordinates) < 2 {
		return false
	}
	first := geometry.Coordinates[0]
	if len(first) < 2 || !finite(first[0]) || !finite(first[1]) {
		return false
	}
	for _, coordinate := range geometry.Coordinates[1:] {
		if len(coordinate) >= 2 && finite(coordinate[0]) && finite(coordinate[1]) && (coordinate[0] != first[0] || coordinate[1] != first[1]) {
			return true
		}
	}
	return false
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func completeCrossWindowConnectedRoadTurns(ctx context.Context, snapshot Snapshot, points []coverage.GeographicObservation, windowStarts []int, portions []matchedPortion, rules coverage.Rules, options coverage.OSMEvaluationOptions) ([]matchedPortion, supplementalStats, error) {
	stats := supplementalStats{}
	for i := 0; i+1 < len(portions); i++ {
		before, after := portions[i], portions[i+1]
		if before.window == after.window || after.window < 0 || after.window >= len(windowStarts) ||
			before.portion.ContinuityClass != "road" || after.portion.ContinuityClass != "road" ||
			before.portion.LogicalPathID == "" || before.portion.LogicalPathID == after.portion.LogicalPathID ||
			before.portion.ToMeter-before.portion.FromMeter < 20 || after.portion.ToMeter-after.portion.FromMeter < 20 ||
			!directedPortionEndIsClipped(before.portion) || !directedPortionStartIsClipped(after.portion) {
			continue
		}
		start := max(0, windowStarts[after.window]-WindowSize/2)
		end := min(len(points), start+WindowSize)
		start = max(0, end-WindowSize)
		rematched, matchStats, err := coverage.MatchOSM(ctx, snapshot, points[start:end], rules, options)
		if err != nil {
			return nil, stats, err
		}
		stats.add(matchStats)
		var flat []coverage.TraversedPortion
		for _, traversal := range rematched.Traversals {
			flat = append(flat, traversal.Portions...)
		}
		for j := 0; j+1 < len(flat); j++ {
			newBefore, newAfter := flat[j], flat[j+1]
			if newBefore.PhysicalSegmentID != before.portion.PhysicalSegmentID || newBefore.Direction != before.portion.Direction ||
				newAfter.PhysicalSegmentID != after.portion.PhysicalSegmentID || newAfter.Direction != after.portion.Direction ||
				newBefore.SourceFromFraction > before.portion.SourceFromFraction+1e-6 || newBefore.SourceToFraction < before.portion.SourceToFraction-1e-6 ||
				newAfter.SourceFromFraction > after.portion.SourceFromFraction+1e-6 || newAfter.SourceToFraction < after.portion.SourceToFraction-1e-6 {
				continue
			}
			added := (newBefore.ToMeter - newBefore.FromMeter) + (newAfter.ToMeter - newAfter.FromMeter) -
				(before.portion.ToMeter - before.portion.FromMeter) - (after.portion.ToMeter - after.portion.FromMeter)
			if added > 1e-6 && added <= 35 {
				portions[i].portion, portions[i+1].portion = newBefore, newAfter
				break
			}
		}
	}
	return portions, stats, nil
}

func replaceCrossWindowRoadTangents(ctx context.Context, snapshot Snapshot, points []coverage.GeographicObservation, windowStarts []int, portions []matchedPortion, rules coverage.Rules, options coverage.OSMEvaluationOptions) ([]matchedPortion, supplementalStats, error) {
	stats := supplementalStats{}
	present := make(map[uuid.UUID]bool)
	for _, item := range portions {
		present[item.portion.PhysicalSegmentID] = true
	}
	for i := 1; i+1 < len(portions); i++ {
		before, selected, after := portions[i-1], portions[i], portions[i+1]
		if before.window != selected.window || selected.window == after.window || after.window < 0 || after.window >= len(windowStarts) ||
			before.portion.ContinuityClass != "road" || selected.portion.ContinuityClass != "road" || after.portion.ContinuityClass != "road" ||
			before.portion.LogicalPathID == "" || selected.portion.LogicalPathID == "" || after.portion.LogicalPathID == "" ||
			before.portion.LogicalPathID == after.portion.LogicalPathID || selected.portion.LogicalPathID == after.portion.LogicalPathID ||
			selected.portion.SourceFromFraction > 1e-6 || selected.portion.SourceToFraction < 1-1e-6 ||
			selected.portion.ToMeter-selected.portion.FromMeter < 20 || selected.portion.ToMeter-selected.portion.FromMeter > 60 {
			continue
		}
		const supplementalWindowSize = 192
		start := max(0, windowStarts[after.window]-128)
		end := min(len(points), start+supplementalWindowSize)
		start = max(0, end-supplementalWindowSize)
		rematched, matchStats, err := coverage.MatchOSM(ctx, snapshot, points[start:end], rules, options)
		if err != nil {
			return nil, stats, err
		}
		stats.add(matchStats)
		var flat []coverage.TraversedPortion
		for _, traversal := range rematched.Traversals {
			flat = append(flat, traversal.Portions...)
		}
		replaced := false
		for left := 0; left < len(flat) && !replaced; left++ {
			if flat[left].PhysicalSegmentID != before.portion.PhysicalSegmentID || flat[left].Direction != before.portion.Direction {
				continue
			}
			for right := left + 3; right < len(flat) && right <= left+6; right++ {
				if flat[right].PhysicalSegmentID != after.portion.PhysicalSegmentID || flat[right].Direction != after.portion.Direction {
					continue
				}
				var replacement []matchedPortion
				portions, replacement = replaceCrossWindowTangent(portions, i, flat[left+1:right], flat[right], before, selected, after, present)
				if len(replacement) > 0 {
					i += len(replacement) - 1
					replaced = true
					break
				}
			}
		}
	}
	return portions, stats, nil
}

func (s *supplementalStats) add(stats coverage.OSMEvaluationStats) {
	s.windows++
	s.candidates += stats.CandidateCount
	s.suppressed += stats.SuppressedPedestrianCandidates
	s.graphNodes += stats.GraphNodeCount
	s.graphEdges += stats.GraphEdgeCount
}

func replaceCrossWindowTangent(portions []matchedPortion, index int, middle []coverage.TraversedPortion, supplementalAfter coverage.TraversedPortion, before, selected, after matchedPortion, present map[uuid.UUID]bool) ([]matchedPortion, []matchedPortion) {
	distance := 0.0
	for i, portion := range middle {
		distance += portion.ToMeter - portion.FromMeter
		selectedTurnaround := len(middle) >= 2 && i < 2 && portion.PhysicalSegmentID == selected.portion.PhysicalSegmentID &&
			middle[0].Direction == selected.portion.Direction && middle[1].Direction != selected.portion.Direction
		if portion.ContinuityClass != "road" || !selectedTurnaround && (portion.PhysicalSegmentID == selected.portion.PhysicalSegmentID || present[portion.PhysicalSegmentID]) {
			return portions, nil
		}
	}
	if len(middle) < 2 || len(middle) > 5 || distance > 500 {
		return portions, nil
	}
	if selected.portion.LogicalPathID == before.portion.LogicalPathID && selected.portion.SourceFromFraction <= 1e-6 && selected.portion.SourceToFraction >= 1-1e-6 {
		returned := selected.portion
		if returned.Direction == coverage.SegmentForward {
			returned.Direction = coverage.SegmentReverse
		} else {
			returned.Direction = coverage.SegmentForward
		}
		middle = append([]coverage.TraversedPortion{selected.portion, returned}, middle...)
		distance += 2 * (selected.portion.ToMeter - selected.portion.FromMeter)
	}
	if len(middle) > 5 || distance > 500 {
		return portions, nil
	}
	replacement := make([]matchedPortion, len(middle))
	for i, portion := range middle {
		replacement[i] = matchedPortion{portion: portion, class: selected.class, window: after.window,
			routeOrder: selected.routeOrder, firstTraversedAt: selected.firstTraversedAt}
		present[portion.PhysicalSegmentID] = true
	}
	portions = append(portions, make([]matchedPortion, len(replacement)-1)...)
	copy(portions[index+len(replacement):], portions[index+1:len(portions)-len(replacement)+1])
	copy(portions[index:], replacement)
	afterIndex := index + len(replacement)
	if supplementalAfter.PhysicalSegmentID == portions[afterIndex].portion.PhysicalSegmentID && supplementalAfter.Direction == portions[afterIndex].portion.Direction {
		merged := portions[afterIndex].portion
		merged.FromMeter = math.Min(merged.FromMeter, supplementalAfter.FromMeter)
		merged.ToMeter = math.Max(merged.ToMeter, supplementalAfter.ToMeter)
		merged.SourceFromFraction = math.Min(merged.SourceFromFraction, supplementalAfter.SourceFromFraction)
		merged.SourceToFraction = math.Max(merged.SourceToFraction, supplementalAfter.SourceToFraction)
		portions[afterIndex].portion = merged
	}
	return portions, replacement
}

func directedPortionEndIsClipped(portion coverage.TraversedPortion) bool {
	if portion.Direction == coverage.SegmentReverse {
		return portion.SourceFromFraction > 1e-6
	}
	return portion.SourceToFraction < 1-1e-6
}

func directedPortionStartIsClipped(portion coverage.TraversedPortion) bool {
	if portion.Direction == coverage.SegmentReverse {
		return portion.SourceToFraction < 1-1e-6
	}
	return portion.SourceFromFraction > 1e-6
}

func traversalClass(traversal coverage.DecodedTraversal) string {
	for _, observation := range traversal.Observations {
		if observation.Status == coverage.ObservationAmbiguous {
			return "ambiguous"
		}
	}
	return "matched"
}
