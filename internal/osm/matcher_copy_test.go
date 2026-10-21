package osm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestMatcherCopySegmentsScopedLogicalPaths(t *testing.T) {
	query := strings.Join(strings.Fields(matcherCopySegmentsSQL), " ")
	for _, fragment := range []string{
		"LEFT JOIN LATERAL ( SELECT min(member.name) AS name,min(member.normalized_name) AS normalized_name FROM osm_active.path_segments member WHERE member.logical_path_id=segment.logical_path_id ) path ON true",
		"segment.region_id=requested.region_id AND segment.generation_id=requested.generation_id AND segment.segment_id=requested.segment_id",
		"locality.region_id=segment.region_id AND locality.generation_id=segment.generation_id AND locality.relation_id=segment.locality_relation_id",
		"COALESCE(path.normalized_name,segment.tags->>'workouts:education_normalized_name')",
		"ST_AsGeoJSON(segment.geom)::jsonb,segment.length_m",
		"ORDER BY requested.ordinal LIMIT $4",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("segment copy SQL missing %q", fragment)
		}
	}
	if strings.Contains(query, "osm_active.logical_paths") {
		t.Error("segment copy still joins the broad logical-path aggregate")
	}
}

func TestMatcherCopySegmentsBoundsBeforeQuery(t *testing.T) {
	valid := MatcherSegmentRef{SegmentID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), RegionID: "fixture:a", GenerationID: 1}
	for _, test := range []struct {
		name  string
		refs  []MatcherSegmentRef
		field string
	}{
		{"empty", nil, "segment copy reference count"},
		{"too many", make([]MatcherSegmentRef, maxClipPortionRefs+1), "segment copy reference count"},
		{"nil segment", []MatcherSegmentRef{{RegionID: valid.RegionID, GenerationID: 1}}, "segment copy provenance"},
		{"empty region", []MatcherSegmentRef{{SegmentID: valid.SegmentID, GenerationID: 1}}, "segment copy provenance"},
		{"zero generation", []MatcherSegmentRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID}}, "segment copy provenance"},
		{"negative generation", []MatcherSegmentRef{{SegmentID: valid.SegmentID, RegionID: valid.RegionID, GenerationID: -1}}, "segment copy provenance"},
		{"duplicate", []MatcherSegmentRef{valid, valid}, "unique segment copy references"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&MatcherSnapshot{}).CopySegments(context.Background(), test.refs)
			var bounds *MatcherBoundsError
			if !errors.As(err, &bounds) || bounds.Field != test.field {
				t.Fatalf("error = %v, want bounds for %q", err, test.field)
			}
		})
	}
	if _, err := (&MatcherSnapshot{}).CopySegments(context.Background(), []MatcherSegmentRef{valid}); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("valid reference error = %v, want pgx.ErrTxClosed", err)
	}
}
