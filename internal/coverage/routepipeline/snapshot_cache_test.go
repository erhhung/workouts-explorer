package routepipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

type memoFixture struct {
	Snapshot
	candidates, incident int
	failure              error
}

func (fixture *memoFixture) Candidates(_ context.Context, points []osm.MatcherObservation, limit int) ([]osm.MatcherCandidate, error) {
	fixture.candidates++
	if fixture.failure != nil {
		return nil, fixture.failure
	}
	var result []osm.MatcherCandidate
	for i, point := range points {
		name := "public fixture"
		result = append(result, osm.MatcherCandidate{ObservationIndex: i, DistanceMeters: point.Longitude + float64(limit), Name: &name, Tags: []byte(`{"public":true}`)})
	}
	return result, nil
}

func (fixture *memoFixture) IncidentEdges(_ context.Context, nodes []uuid.UUID, limit int) ([]osm.MatcherIncidentEdge, error) {
	fixture.incident++
	if fixture.failure != nil {
		return nil, fixture.failure
	}
	if len(nodes) > limit {
		return nil, &osm.MatcherOverflowError{Operation: "incident edges", Limit: limit}
	}
	var result []osm.MatcherIncidentEdge
	for _, node := range nodes {
		result = append(result, osm.MatcherIncidentEdge{RequestedNodeID: node, Tags: []byte(`{"public":true}`)})
	}
	return result, nil
}

func TestSnapshotMemoReindexesSubsetsWithoutSharingMutableRows(t *testing.T) {
	ctx := context.Background()
	fixture := &memoFixture{}
	cache := newCachedSnapshot(fixture)
	points := []osm.MatcherObservation{{Longitude: 1, RadiusMeters: 20}, {Longitude: 2, RadiusMeters: 20}}
	rows, err := cache.Candidates(ctx, points, 16)
	if err != nil {
		t.Fatal(err)
	}
	rows[1].Tags[0], *rows[1].Name = 'x', "changed"
	subset, err := cache.Candidates(ctx, []osm.MatcherObservation{points[1], points[0], points[1]}, 16)
	if err != nil || fixture.candidates != 1 || len(subset) != 3 {
		t.Fatal("cached subset requested the database", fixture.candidates, err)
	}
	for i, row := range subset {
		if row.ObservationIndex != i || row.Tags[0] != '{' || *row.Name != "public fixture" || row.DistanceMeters != []float64{18, 17, 18}[i] {
			t.Fatal("cache changed source order/metadata", subset)
		}
	}
	*subset[0].Name = "mutated again"
	fresh, _ := cache.Candidates(ctx, points[1:], 16)
	if *fresh[0].Name != "public fixture" {
		t.Fatal("caller changed a retained cache value")
	}
	points[0].RadiusMeters++
	_, _ = cache.Candidates(ctx, points, 16)
	if fixture.candidates != 2 {
		t.Fatal("radius was omitted from candidate identity")
	}
	_, _ = cache.Candidates(ctx, points, 17)
	if fixture.candidates != 3 {
		t.Fatal("candidate limit was omitted from candidate identity")
	}
}

func TestSnapshotMemoPreservesIncidentTotalBoundsAndRequestOrder(t *testing.T) {
	ctx := context.Background()
	fixture := &memoFixture{}
	cache := newCachedSnapshot(fixture)
	a, b := uuid.New(), uuid.New()
	rows, err := cache.IncidentEdges(ctx, []uuid.UUID{a, b}, 100)
	if err != nil {
		t.Fatal(err)
	}
	rows[0].Tags[0] = 'x'
	reordered, err := cache.IncidentEdges(ctx, []uuid.UUID{b, a}, 2)
	if err != nil || fixture.incident != 1 || reordered[0].RequestedNodeID != b || reordered[1].RequestedNodeID != a || reordered[1].Tags[0] != '{' {
		t.Fatal("incident cache lost completeness/order/ownership", reordered, err)
	}
	_, err = cache.IncidentEdges(ctx, []uuid.UUID{a, b}, 1)
	var overflow *osm.MatcherOverflowError
	if !errors.As(err, &overflow) || fixture.incident != 2 {
		t.Fatal("cached rows bypassed the total response bound", err)
	}
	_, _ = cache.IncidentEdges(ctx, []uuid.UUID{a, a}, 2)
	if fixture.incident != 3 {
		t.Fatal("duplicate request did not retain original backend semantics")
	}
}

func TestSnapshotMemoDoesNotCacheFailuresOrCrossSnapshots(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("fixture read failed")
	fixture := &memoFixture{failure: failure}
	cache := newCachedSnapshot(fixture)
	points := []osm.MatcherObservation{{RadiusMeters: 20}}
	for range 2 {
		if _, err := cache.Candidates(ctx, points, 16); err != failure {
			t.Fatal(err)
		}
	}
	if fixture.candidates != 2 {
		t.Fatal("a failed lookup became cached evidence")
	}
	fixture.failure = nil
	first, _ := cache.Candidates(ctx, points, 16)
	secondCache := newCachedSnapshot(fixture)
	second, _ := secondCache.Candidates(ctx, points, 16)
	if fixture.candidates != 4 || !reflect.DeepEqual(first, second) {
		t.Fatal("cache crossed a route/snapshot boundary")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cache.Candidates(canceled, points, 16); err != context.Canceled {
		t.Fatal("cache ignored cancellation", err)
	}
}

func TestSnapshotMemoBudgetsEvictOrDeclineWithoutChangingResults(t *testing.T) {
	if reflect.TypeFor[osm.MatcherCandidate]().Size()+128 > snapshotCacheRowBytes || reflect.TypeFor[osm.MatcherIncidentEdge]().Size()+128 > snapshotCacheRowBytes {
		t.Fatal("snapshot cache row outgrew its conservative charge")
	}
	fixture := &memoFixture{}
	cache := newCachedSnapshot(fixture)
	cache.observations.limit, cache.observations.slots = 2048, 1
	for _, longitude := range []float64{1, 2, 1} {
		rows, err := cache.Candidates(context.Background(), []osm.MatcherObservation{{Longitude: longitude, RadiusMeters: 20}}, 16)
		if err != nil || rows[0].DistanceMeters != longitude+16 || cache.observations.bytes > cache.observations.limit || len(cache.observations.entries) > 1 {
			t.Fatal("bounded cache changed output or exceeded its budget", err)
		}
	}
	if fixture.candidates != 3 {
		t.Fatal("LRU did not evict stale entries")
	}
	cache.observations.limit = 1
	rows, err := cache.Candidates(context.Background(), []osm.MatcherObservation{{Longitude: 3, RadiusMeters: 20}}, 16)
	if err != nil || rows[0].DistanceMeters != 19 {
		t.Fatal("optional cache became a processing limit", err)
	}
}
