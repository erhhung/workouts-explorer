package osm

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMatcherCandidateBoundsBeforeQuery(t *testing.T) {
	tests := []struct {
		name         string
		observations []MatcherObservation
		limit        int
		field        string
	}{
		{name: "empty", limit: 1, field: "observation count"},
		{name: "too many", observations: make([]MatcherObservation, maxMatcherObservations+1), limit: 1, field: "observation count"},
		{name: "zero limit", observations: validMatcherObservations(), field: "candidate limit"},
		{name: "large limit", observations: validMatcherObservations(), limit: maxCandidatesPerPoint + 1, field: "candidate limit"},
		{name: "nan longitude", observations: []MatcherObservation{{Longitude: math.NaN(), RadiusMeters: 1}}, limit: 1, field: "coordinates"},
		{name: "longitude range", observations: []MatcherObservation{{Longitude: 181, RadiusMeters: 1}}, limit: 1, field: "coordinates"},
		{name: "latitude range", observations: []MatcherObservation{{Latitude: -91, RadiusMeters: 1}}, limit: 1, field: "coordinates"},
		{name: "nan radius", observations: []MatcherObservation{{RadiusMeters: math.NaN()}}, limit: 1, field: "radius meters"},
		{name: "zero radius", observations: []MatcherObservation{{RadiusMeters: 0}}, limit: 1, field: "radius meters"},
		{name: "large radius", observations: []MatcherObservation{{RadiusMeters: maxCandidateRadiusM + 1}}, limit: 1, field: "radius meters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&MatcherSnapshot{}).Candidates(context.Background(), test.observations, test.limit)
			var bounds *MatcherBoundsError
			if !errors.As(err, &bounds) || bounds.Field != test.field {
				t.Fatalf("error = %v, want MatcherBoundsError for %q", err, test.field)
			}
		})
	}
}

func TestMatcherCopyUsesEducationNameOnlyAsFallback(t *testing.T) {
	for _, fragment := range []string{
		"COALESCE(segment.name,segment.tags->>'workouts:education_name')",
		"COALESCE(segment.normalized_name,segment.tags->>'workouts:education_normalized_name')",
		"COALESCE(path.name,segment.tags->>'workouts:education_name')",
	} {
		if !strings.Contains(matcherCopySegmentsSQL, fragment) {
			t.Errorf("matcher segment copy missing education fallback %q", fragment)
		}
	}
}

func TestMatcherIncidentBoundsBeforeQuery(t *testing.T) {
	validNode := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tests := []struct {
		name  string
		nodes []uuid.UUID
		limit int
		field string
	}{
		{name: "empty", limit: 1, field: "incident node count"},
		{name: "too many", nodes: make([]uuid.UUID, maxIncidentNodes+1), limit: 1, field: "incident node count"},
		{name: "zero edge limit", nodes: []uuid.UUID{validNode}, field: "incident edge limit"},
		{name: "large edge limit", nodes: []uuid.UUID{validNode}, limit: maxIncidentEdges + 1, field: "incident edge limit"},
		{name: "nil node", nodes: []uuid.UUID{uuid.Nil}, limit: 1, field: "incident node IDs"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&MatcherSnapshot{}).IncidentEdges(context.Background(), test.nodes, test.limit)
			var bounds *MatcherBoundsError
			if !errors.As(err, &bounds) || bounds.Field != test.field {
				t.Fatalf("error = %v, want MatcherBoundsError for %q", err, test.field)
			}
		})
	}
}

func TestMatcherClipBoundsBeforeQuery(t *testing.T) {
	valid := MatcherPortionRef{
		SegmentID: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		RegionID:  "fixture:region", GenerationID: 1, Direction: MatcherForward,
		SourceFromFraction: 0.2, SourceToFraction: 0.8,
	}
	tests := []struct {
		name  string
		refs  []MatcherPortionRef
		field string
	}{
		{name: "empty", field: "clip portion reference count"},
		{name: "too many", refs: make([]MatcherPortionRef, maxClipPortionRefs+1), field: "clip portion reference count"},
		{name: "nil segment", refs: []MatcherPortionRef{{RegionID: valid.RegionID, GenerationID: 1, Direction: MatcherForward, SourceToFraction: 1}}, field: "clip portion provenance"},
		{name: "empty region", refs: []MatcherPortionRef{{SegmentID: valid.SegmentID, GenerationID: 1, Direction: MatcherForward, SourceToFraction: 1}}, field: "clip portion provenance"},
		{name: "zero generation", refs: []MatcherPortionRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID, Direction: MatcherForward, SourceToFraction: 1}}, field: "clip portion provenance"},
		{name: "nan fraction", refs: []MatcherPortionRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID, GenerationID: 1, Direction: MatcherForward, SourceFromFraction: math.NaN(), SourceToFraction: 1}}, field: "clip portion fractions"},
		{name: "fraction below zero", refs: []MatcherPortionRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID, GenerationID: 1, Direction: MatcherForward, SourceFromFraction: -0.1, SourceToFraction: 1}}, field: "clip portion fractions"},
		{name: "fraction above one", refs: []MatcherPortionRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID, GenerationID: 1, Direction: MatcherForward, SourceToFraction: 1.1}}, field: "clip portion fractions"},
		{name: "zero interval", refs: []MatcherPortionRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID, GenerationID: 1, Direction: MatcherForward, SourceFromFraction: 0.5, SourceToFraction: 0.5}}, field: "clip portion fractions"},
		{name: "invalid direction", refs: []MatcherPortionRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID, GenerationID: 1, Direction: "sideways", SourceToFraction: 1}}, field: "clip portion direction"},
		{name: "duplicate", refs: []MatcherPortionRef{valid, valid}, field: "unique clip portion references"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&MatcherSnapshot{}).ClipPortions(context.Background(), test.refs)
			var bounds *MatcherBoundsError
			if !errors.As(err, &bounds) || bounds.Field != test.field {
				t.Fatalf("error = %v, want MatcherBoundsError for %q", err, test.field)
			}
		})
	}
	if _, err := (&MatcherSnapshot{}).ClipPortions(context.Background(), []MatcherPortionRef{valid}); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("valid ref error = %v, want pgx.ErrTxClosed", err)
	}
}

func TestMatcherErrorsContainNoCoordinates(t *testing.T) {
	observation := MatcherObservation{Longitude: 123.456789, Latitude: 45.678912, RadiusMeters: 251.2345}
	_, err := (&MatcherSnapshot{}).Candidates(context.Background(), []MatcherObservation{observation}, 1)
	for _, privateValue := range []string{"123.456789", "45.678912", "251.2345"} {
		if strings.Contains(err.Error(), privateValue) {
			t.Errorf("error exposes input value %s: %v", privateValue, err)
		}
	}
	index := 7
	overflow := &MatcherOverflowError{Operation: "candidates", Limit: 2, ObservationIndex: &index}
	if strings.Contains(overflow.Error(), "longitude") || strings.Contains(overflow.Error(), "latitude") {
		t.Fatalf("overflow error exposes coordinates: %v", overflow)
	}
}

func TestMatcherSQLContracts(t *testing.T) {
	for _, fragment := range []string{
		"ST_DWithin(segment.geom::geography,observation.location::geography,observation.radius_m)",
		"ST_LineLocatePoint",
		"ST_LineInterpolatePoint",
		"ST_Azimuth",
		"> (segment.source_way_version,segment.generation_id,segment.region_id)",
		"> (segment.generation_id,segment.region_id)",
		"LIMIT $4",
		"ORDER BY observation.observation_index,candidate.distance_m,candidate.segment_id",
	} {
		if !strings.Contains(matcherCandidatesSQL, fragment) {
			t.Errorf("candidate SQL is missing %q", fragment)
		}
	}
	if strings.Contains(matcherCandidatesSQL, "JOIN osm_catalog.generations") || strings.Contains(matcherIncidentEdgesSQL, "JOIN osm_catalog.generations") {
		t.Error("matcher SQL redundantly joins generation state even though canonical partitions contain only active rows")
	}
	for _, fragment := range []string{
		"segment.start_graph_node_id=node.node_id",
		"UNION\n",
		"segment.end_graph_node_id=node.node_id",
		"ORDER BY key.ordinal,segment.segment_id,segment.generation_id DESC,segment.region_id DESC",
		"LIMIT $2",
	} {
		if !strings.Contains(matcherIncidentEdgesSQL, fragment) {
			t.Errorf("incident-edge SQL is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"WITH ORDINALITY",
		"segment.region_id=requested.region_id",
		"segment.generation_id=requested.generation_id",
		"segment.segment_id=requested.segment_id",
		"ST_LineSubstring(segment.geom,requested.source_from_fraction,requested.source_to_fraction)",
		"ST_Reverse(",
		"ST_AsGeoJSON(geom)::jsonb",
		"ST_Length(geom::geography)",
		"ORDER BY ordinal",
		"LIMIT $7",
	} {
		if !strings.Contains(matcherClipPortionsSQL, fragment) {
			t.Errorf("clip-portion SQL is missing %q", fragment)
		}
	}
	for _, query := range []string{matcherCandidatesSQL, matcherIncidentEdgesSQL, matcherClipPortionsSQL} {
		if strings.Contains(query, "osm_active.path_segments") || strings.Contains(query, "INSERT ") || strings.Contains(query, "UPDATE ") {
			t.Errorf("matcher query bypasses canonical provenance or mutates data")
		}
	}
}

func TestMatcherSnapshotRequiresTransaction(t *testing.T) {
	_, err := (&MatcherSnapshot{}).Generations(context.Background())
	if !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("Generations error = %v, want pgx.ErrTxClosed", err)
	}
	if err := (&MatcherSnapshot{}).Close(context.Background()); err != nil {
		t.Fatalf("Close empty snapshot: %v", err)
	}
}

func TestUnrestrictedMatcherQueriesTreatNullGenerationArraysAsEmpty(t *testing.T) {
	for name, query := range map[string]string{
		"candidates":     matcherCandidatesSQL,
		"incident edges": matcherIncidentEdgesSQL,
	} {
		if strings.Contains(query, "AND (cardinality(") || !strings.Contains(query, "COALESCE(cardinality(") {
			t.Errorf("%s query does not treat a null generation array as unrestricted", name)
		}
	}
}

func TestMatcherReadOnlySnapshotIntegration(t *testing.T) {
	databaseURL := os.Getenv("OSM_MATCHER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("OSM_MATCHER_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	snapshot, err := BeginMatcherSnapshot(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close(context.Background())
	var isolation, readOnly string
	if err := snapshot.tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&isolation); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
		t.Fatal(err)
	}
	if isolation != "repeatable read" || readOnly != "on" {
		t.Fatalf("snapshot is isolation=%q read_only=%q", isolation, readOnly)
	}
	generations, err := snapshot.Generations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, generation := range generations {
		if len(generation.SourceSHA256) != 32 {
			t.Fatalf("generation digest length=%d", len(generation.SourceSHA256))
		}
	}
	var fixture bool
	if err := snapshot.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM osm_catalog.regions WHERE id='fixture:region-a')`).Scan(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture {
		var fixtureGeneration int64
		if err := snapshot.tx.QueryRow(ctx, `SELECT id FROM osm_catalog.generations WHERE region_id='fixture:region-a' AND state='active'`).Scan(&fixtureGeneration); err != nil {
			t.Fatal(err)
		}
		candidates, err := snapshot.Candidates(ctx, []MatcherObservation{{Longitude: 0.5, Latitude: 1, RadiusMeters: 10}}, 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 || candidates[0].SegmentID != uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd") ||
			candidates[0].RegionID != "fixture:region-a" || candidates[0].GenerationID != fixtureGeneration ||
			candidates[0].Name == nil || *candidates[0].Name != "New Path" || len(candidates[0].Tags) == 0 ||
			candidates[0].StartLongitude != 0 || candidates[0].EndLongitude != 1 {
			t.Fatalf("fixture candidates=%+v", candidates)
		}
		edges, err := snapshot.IncidentEdges(ctx, []uuid.UUID{candidates[0].StartGraphNodeID}, 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(edges) != 1 || edges[0].SegmentID != candidates[0].SegmentID || edges[0].RequestedNodeID != candidates[0].StartGraphNodeID {
			t.Fatalf("fixture incident edges=%+v", edges)
		}
		clipped, err := snapshot.ClipPortions(ctx, []MatcherPortionRef{{
			SegmentID: candidates[0].SegmentID, RegionID: candidates[0].RegionID,
			GenerationID: candidates[0].GenerationID, Direction: MatcherReverse,
			SourceFromFraction: 0.2, SourceToFraction: 0.8,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(clipped) != 1 || clipped[0].Ordinal != 0 || clipped[0].Direction != MatcherReverse ||
			clipped[0].SegmentID != candidates[0].SegmentID || clipped[0].SourceWayID != candidates[0].SourceWayID ||
			clipped[0].LogicalPathID != candidates[0].LogicalPathID || len(clipped[0].GeoJSON) == 0 || clipped[0].LengthMeters <= 0 {
			t.Fatalf("fixture clipped portions=%+v", clipped)
		}
		copies, err := snapshot.CopySegments(ctx, []MatcherSegmentRef{{SegmentID: candidates[0].SegmentID,
			RegionID: candidates[0].RegionID, GenerationID: candidates[0].GenerationID}})
		if err != nil {
			t.Fatal(err)
		}
		if len(copies) != 1 || copies[0].Ordinal != 0 || copies[0].SegmentID != candidates[0].SegmentID ||
			copies[0].LogicalPathID != candidates[0].LogicalPathID || copies[0].PathName == nil ||
			*copies[0].PathName != "New Path" || len(copies[0].GeoJSON) == 0 || copies[0].LengthMeters <= 0 {
			t.Fatalf("fixture segment copies=%+v", copies)
		}
		targeted, err := BeginMatcherSnapshotForGenerations(ctx, pool, []MatcherGeneration{{
			RegionID: "fixture:region-a", GenerationID: fixtureGeneration,
		}})
		if err != nil {
			t.Fatal(err)
		}
		targetedCandidates, err := targeted.Candidates(ctx, []MatcherObservation{{Longitude: 0.5, Latitude: 1, RadiusMeters: 10}}, 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(targetedCandidates) != 1 || targetedCandidates[0].RegionID != "fixture:region-a" ||
			targetedCandidates[0].GenerationID != fixtureGeneration {
			t.Fatalf("targeted fixture candidates=%+v", targetedCandidates)
		}
		if err := targeted.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := BeginMatcherSnapshotForGenerations(ctx, pool, []MatcherGeneration{{
			RegionID: "fixture:region-a", GenerationID: fixtureGeneration + 1,
		}}); !errors.Is(err, ErrMatcherGenerationChanged) {
			t.Fatalf("inactive target generation error=%v", err)
		}
	}
	// A synthetic polar point exercises the geographic candidate SQL without
	// depending on, or disclosing, coordinates from the database.
	if _, err := snapshot.Candidates(ctx, []MatcherObservation{{Longitude: 0, Latitude: 90, RadiusMeters: 1}}, 1); err != nil {
		t.Fatal(err)
	}
	var nodeID uuid.UUID
	err = snapshot.tx.QueryRow(ctx, `
		SELECT segment.start_graph_node_id
		FROM osm_canonical.path_segments segment
		JOIN osm_catalog.generations generation
		  ON generation.id=segment.generation_id AND generation.region_id=segment.region_id
		WHERE generation.state='active' LIMIT 1`).Scan(&nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.IncidentEdges(ctx, []uuid.UUID{nodeID}, maxIncidentEdges); err != nil {
		t.Fatal(err)
	}
}

func validMatcherObservations() []MatcherObservation {
	return []MatcherObservation{{Longitude: -122, Latitude: 37, RadiusMeters: 25}}
}
