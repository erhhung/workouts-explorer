package coverage

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestOSMExpansionLimitsAreTyped(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*OSMEvaluationOptions)
		field     string
		limit     int
	}{
		{"nodes", func(options *OSMEvaluationOptions) { options.MaxGraphNodes = 1 }, "graph node", 1},
		{"edges", func(options *OSMEvaluationOptions) { options.MaxGraphEdges = 1 }, "graph edge", 1},
		{"layers", func(options *OSMEvaluationOptions) { options.MaxExpansionLayers = 0 }, "graph layer", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := DefaultOSMEvaluationOptions()
			test.configure(&options)
			segments := map[uuid.UUID]physicalSegment{
				testPhysicalSegment: {startNode: testStartNode, endNode: testEndNode},
			}
			err := expandOSMGraph(context.Background(), &fakeOSMTopology{}, segments,
				experimentalPathPolicy(MovementSharedPublic), options, 500, &OSMEvaluationStats{})
			var limit *OSMEvaluationLimitError
			if !errors.As(err, &limit) || limit.Field != test.field || limit.Limit != test.limit {
				t.Fatalf("wrong graph limit: %T", err)
			}
		})
	}
}
