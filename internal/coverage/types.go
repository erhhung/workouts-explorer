// Package coverage contains an experimental, side-effect-free map matcher.
// It deliberately has no database, worker, or service integration.
package coverage

import (
	"time"

	"github.com/google/uuid"
)

type Point struct {
	X, Y float64
}

type Observation struct {
	Sequence        int
	Point           Point
	Time            time.Time
	AccuracyMeters  float64
	HeadingDegrees  *float64
	HeadingAccuracy *float64
}

// DirectedSegment is one permitted direction through a physical segment.
type DirectedSegment struct {
	ID                    string
	PhysicalID            string
	PhysicalSegmentID     uuid.UUID
	FromNode              string
	ToNode                string
	From                  Point
	To                    Point
	LengthMeters          float64
	LogicalPathID         string
	LocalityID            string
	RegionID              string
	GenerationID          int64
	DerivationVersion     int
	SourceWayID           int64
	SourceWayVersion      int
	Direction             SegmentDirection
	TransitionOnly        bool
	ContinuityConnector   bool
	DrivewayConnector     bool
	AccessoryConnector    bool
	ParkingAisleConnector bool
	RoadConnector         bool
	IndependentPath       bool
	ContinuityClass       string
}

type SegmentDirection string

const (
	SegmentForward SegmentDirection = "forward"
	SegmentReverse SegmentDirection = "reverse"
)

// Candidate is a supplied projection onto a directed segment. AlongMeters is
// measured from the segment's From endpoint.
type Candidate struct {
	SegmentID               string
	Projected               Point
	AlongMeters             float64
	DistanceMeters          float64
	AttributionOffsetMeters float64
	TangentDegrees          *float64
	PathGroupID             string
	PathSwitchCost          float64
	PhysicalGroupID         string
	UTurnCost               float64
	RawDistanceCostWeight   float64
	ContinuityClass         string
	ContinuityClassCost     float64
}

type Graph struct {
	Segments                       []DirectedSegment
	MaxContinuityConnectorMeters   float64
	MaxDrivewayConnectorMeters     float64
	MaxAccessoryConnectorMeters    float64
	MaxParkingAisleConnectorMeters float64
}

type ObservationStatus string

const (
	ObservationUnmatched ObservationStatus = "unmatched"
	ObservationRejected  ObservationStatus = "rejected"
	ObservationMatched   ObservationStatus = "matched"
	ObservationAmbiguous ObservationStatus = "ambiguous"
)

type SplitReason string

const (
	SplitNoCandidate SplitReason = "no_candidate"
	SplitTemporal    SplitReason = "temporal_gap"
	SplitSpatial     SplitReason = "spatial_gap"
	SplitNetwork     SplitReason = "network_gap"
)

type Split struct {
	BeforeObservation int
	Reason            SplitReason
}

type DecodedObservation struct {
	ObservationIndex int
	Status           ObservationStatus
	Candidate        *Candidate
}

// TraversedPortion is a directed, positive-length interval on one segment.
type TraversedPortion struct {
	SegmentID          string
	FromMeter          float64
	ToMeter            float64
	PhysicalSegmentID  uuid.UUID
	RegionID           string
	GenerationID       int64
	DerivationVersion  int
	LogicalPathID      string
	LocalityID         string
	SourceWayID        int64
	SourceWayVersion   int
	Direction          SegmentDirection
	SourceFromFraction float64
	SourceToFraction   float64
	ContinuityClass    string
}

type DecodedTraversal struct {
	FirstObservation int
	LastObservation  int
	Observations     []DecodedObservation
	Portions         []TraversedPortion
	Cost             float64
}

type Result struct {
	RulesVersion           RulesVersion
	MinimumTraversalMeters float64
	Observations           []DecodedObservation
	Traversals             []DecodedTraversal
	Splits                 []Split
}
