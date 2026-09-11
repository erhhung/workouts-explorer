package osm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxMatcherObservations = 256
	maxCandidatesPerPoint  = 256
	maxCandidateRadiusM    = 250
	maxIncidentNodes       = 256
	maxIncidentEdges       = 4096
	maxClipPortionRefs     = 4096
)

type MatcherObservation struct {
	Longitude    float64
	Latitude     float64
	RadiusMeters float64
}

type MatcherCandidate struct {
	ObservationIndex    int
	SegmentID           uuid.UUID
	RegionID            string
	GenerationID        int64
	SourceWayID         int64
	SourceWayVersion    int
	DerivationVersion   int
	StartGraphNodeID    uuid.UUID
	EndGraphNodeID      uuid.UUID
	LogicalPathID       uuid.UUID
	LocalityRelationID  *int64
	Name                *string
	NormalizedName      *string
	Highway             string
	BroadClass          string
	Tags                json.RawMessage
	MotorForwardAllowed bool
	MotorReverseAllowed bool
	LengthMeters        float64
	DistanceMeters      float64
	Fraction            float64
	ProjectedLongitude  float64
	ProjectedLatitude   float64
	TangentDegrees      *float64
	StartLongitude      float64
	StartLatitude       float64
	EndLongitude        float64
	EndLatitude         float64
}

type MatcherIncidentEdge struct {
	RequestedNodeID     uuid.UUID
	SegmentID           uuid.UUID
	RegionID            string
	GenerationID        int64
	SourceWayID         int64
	SourceWayVersion    int
	DerivationVersion   int
	StartGraphNodeID    uuid.UUID
	EndGraphNodeID      uuid.UUID
	LogicalPathID       uuid.UUID
	LocalityRelationID  *int64
	Name                *string
	NormalizedName      *string
	Highway             string
	BroadClass          string
	Tags                json.RawMessage
	MotorForwardAllowed bool
	MotorReverseAllowed bool
	LengthMeters        float64
	StartLongitude      float64
	StartLatitude       float64
	EndLongitude        float64
	EndLatitude         float64
}

type MatcherGeneration struct {
	RegionID              string
	GenerationID          int64
	SourceURL             string
	SourceSHA256          []byte
	SourceHeaderTimestamp *time.Time
	ImporterVersion       int
	DerivationVersion     int
	PromotedAt            time.Time
}

type MatcherUnavailableRegion struct {
	RegionID    string
	DisplayName string
}

type MatcherDirection string

const (
	MatcherForward MatcherDirection = "forward"
	MatcherReverse MatcherDirection = "reverse"
)

// MatcherPortionRef identifies an interval on one exact canonical segment.
// Fractions are an ascending interval on the segment's stored LineString;
// Direction controls the travel order of the returned geometry.
type MatcherPortionRef struct {
	SegmentID          uuid.UUID
	RegionID           string
	GenerationID       int64
	Direction          MatcherDirection
	SourceFromFraction float64
	SourceToFraction   float64
}

type MatcherClippedPortion struct {
	Ordinal            int
	SegmentID          uuid.UUID
	RegionID           string
	GenerationID       int64
	DerivationVersion  int
	LogicalPathID      uuid.UUID
	LocalityRelationID *int64
	SourceWayID        int64
	SourceWayVersion   int
	Direction          MatcherDirection
	SourceFromFraction float64
	SourceToFraction   float64
	GeoJSON            json.RawMessage
	LengthMeters       float64
}

// MatcherBoundsError reports rejected request shape without retaining input data.
type MatcherBoundsError struct {
	Field string
	Limit int
}

func (e *MatcherBoundsError) Error() string {
	return fmt.Sprintf("OSM matcher %s exceeds its bound of %d", e.Field, e.Limit)
}

// MatcherOverflowError reports a bounded max+1 query without exposing coordinates.
type MatcherOverflowError struct {
	Operation        string
	Limit            int
	ObservationIndex *int
}

func (e *MatcherOverflowError) Error() string {
	if e.ObservationIndex != nil {
		return fmt.Sprintf("OSM matcher %s overflow for observation %d (limit %d)", e.Operation, *e.ObservationIndex, e.Limit)
	}
	return fmt.Sprintf("OSM matcher %s overflow (limit %d)", e.Operation, e.Limit)
}

type MatcherSnapshot struct {
	tx     pgx.Tx
	closed bool
}

func BeginMatcherSnapshot(ctx context.Context, pool *pgxpool.Pool) (*MatcherSnapshot, error) {
	if pool == nil {
		return nil, errors.New("OSM database is unavailable")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin OSM matcher snapshot: %w", err)
	}
	return &MatcherSnapshot{tx: tx}, nil
}

func (s *MatcherSnapshot) Commit(ctx context.Context) error {
	if s == nil || s.tx == nil || s.closed {
		return pgx.ErrTxClosed
	}
	s.closed = true
	if err := s.tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OSM matcher snapshot: %w", err)
	}
	return nil
}

func (s *MatcherSnapshot) Rollback(ctx context.Context) error {
	if s == nil || s.tx == nil || s.closed {
		return pgx.ErrTxClosed
	}
	s.closed = true
	if err := s.tx.Rollback(ctx); err != nil {
		return fmt.Errorf("roll back OSM matcher snapshot: %w", err)
	}
	return nil
}

// Close rolls back an uncommitted snapshot and is safe to defer.
func (s *MatcherSnapshot) Close(ctx context.Context) error {
	if s == nil || s.tx == nil || s.closed {
		return nil
	}
	err := s.Rollback(ctx)
	if errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}
	return err
}

const matcherCandidatesSQL = `
WITH observations AS (
    SELECT ordinal::integer-1 AS observation_index,
           ST_SetSRID(ST_MakePoint(longitude,latitude),4326) AS location,
           radius_m
    FROM unnest($1::double precision[],$2::double precision[],$3::double precision[])
         WITH ORDINALITY AS input(longitude,latitude,radius_m,ordinal)
)
SELECT observation.observation_index,candidate.segment_id,candidate.region_id,
       candidate.generation_id,candidate.source_way_id,candidate.source_way_version,
       candidate.derivation_version,candidate.start_graph_node_id,candidate.end_graph_node_id,
       candidate.logical_path_id,candidate.locality_relation_id,candidate.name,candidate.normalized_name,
       candidate.highway,candidate.broad_class,candidate.tags,
       candidate.motor_forward_allowed,candidate.motor_reverse_allowed,candidate.length_m,
       candidate.distance_m,candidate.fraction,ST_X(candidate.projected),ST_Y(candidate.projected),
       degrees(ST_Azimuth(
           ST_LineInterpolatePoint(candidate.geom,greatest(0,candidate.fraction-least(0.01,1/greatest(candidate.length_m,1)))),
           ST_LineInterpolatePoint(candidate.geom,least(1,candidate.fraction+least(0.01,1/greatest(candidate.length_m,1))))
        )) AS tangent_degrees,
       ST_X(ST_StartPoint(candidate.geom)),ST_Y(ST_StartPoint(candidate.geom)),
       ST_X(ST_EndPoint(candidate.geom)),ST_Y(ST_EndPoint(candidate.geom))
FROM observations observation
CROSS JOIN LATERAL (
    SELECT located.*,ST_LineInterpolatePoint(located.geom,located.fraction) AS projected
    FROM (
        SELECT segment.*,
               ST_Distance(segment.geom::geography,observation.location::geography) AS distance_m,
               ST_LineLocatePoint(segment.geom,observation.location) AS fraction
        FROM osm_canonical.path_segments segment
        WHERE ST_DWithin(segment.geom::geography,observation.location::geography,observation.radius_m)
          AND NOT EXISTS (
              SELECT 1 FROM osm_canonical.ways preferred_way
              WHERE preferred_way.way_id=segment.source_way_id
                AND (preferred_way.version,preferred_way.generation_id,preferred_way.region_id)
                    > (segment.source_way_version,segment.generation_id,segment.region_id)
          )
          AND NOT EXISTS (
              SELECT 1 FROM osm_canonical.path_segments preferred_segment
              WHERE preferred_segment.segment_id=segment.segment_id
                AND (preferred_segment.generation_id,preferred_segment.region_id)
                    > (segment.generation_id,segment.region_id)
          )
        ORDER BY distance_m,segment.segment_id,segment.generation_id DESC,segment.region_id DESC
        LIMIT $4
    ) located
) candidate
ORDER BY observation.observation_index,candidate.distance_m,candidate.segment_id,
         candidate.generation_id DESC,candidate.region_id DESC`

func (s *MatcherSnapshot) Candidates(ctx context.Context, observations []MatcherObservation, maxPerObservation int) ([]MatcherCandidate, error) {
	if len(observations) == 0 || len(observations) > maxMatcherObservations {
		return nil, &MatcherBoundsError{Field: "observation count", Limit: maxMatcherObservations}
	}
	if maxPerObservation < 1 || maxPerObservation > maxCandidatesPerPoint {
		return nil, &MatcherBoundsError{Field: "candidate limit", Limit: maxCandidatesPerPoint}
	}
	longitudes := make([]float64, len(observations))
	latitudes := make([]float64, len(observations))
	radii := make([]float64, len(observations))
	for i, observation := range observations {
		if !finite(observation.Longitude) || observation.Longitude < -180 || observation.Longitude > 180 ||
			!finite(observation.Latitude) || observation.Latitude < -90 || observation.Latitude > 90 {
			return nil, &MatcherBoundsError{Field: "coordinates", Limit: maxMatcherObservations}
		}
		if !finite(observation.RadiusMeters) || observation.RadiusMeters <= 0 || observation.RadiusMeters > maxCandidateRadiusM {
			return nil, &MatcherBoundsError{Field: "radius meters", Limit: maxCandidateRadiusM}
		}
		longitudes[i], latitudes[i], radii[i] = observation.Longitude, observation.Latitude, observation.RadiusMeters
	}
	if len(longitudes) != len(latitudes) || len(longitudes) != len(radii) {
		return nil, &MatcherBoundsError{Field: "observation array lengths", Limit: maxMatcherObservations}
	}
	if err := s.queryable(); err != nil {
		return nil, err
	}
	rows, err := s.tx.Query(ctx, matcherCandidatesSQL, longitudes, latitudes, radii, maxPerObservation+1)
	if err != nil {
		return nil, fmt.Errorf("query OSM matcher candidates: %w", err)
	}
	defer rows.Close()
	result := make([]MatcherCandidate, 0)
	counts := make([]int, len(observations))
	for rows.Next() {
		var candidate MatcherCandidate
		if err := rows.Scan(
			&candidate.ObservationIndex, &candidate.SegmentID, &candidate.RegionID,
			&candidate.GenerationID, &candidate.SourceWayID, &candidate.SourceWayVersion,
			&candidate.DerivationVersion, &candidate.StartGraphNodeID, &candidate.EndGraphNodeID,
			&candidate.LogicalPathID, &candidate.LocalityRelationID, &candidate.Name, &candidate.NormalizedName,
			&candidate.Highway, &candidate.BroadClass, &candidate.Tags,
			&candidate.MotorForwardAllowed, &candidate.MotorReverseAllowed, &candidate.LengthMeters,
			&candidate.DistanceMeters, &candidate.Fraction, &candidate.ProjectedLongitude,
			&candidate.ProjectedLatitude, &candidate.TangentDegrees,
			&candidate.StartLongitude, &candidate.StartLatitude, &candidate.EndLongitude, &candidate.EndLatitude,
		); err != nil {
			return nil, fmt.Errorf("scan OSM matcher candidate: %w", err)
		}
		if candidate.ObservationIndex < 0 || candidate.ObservationIndex >= len(counts) {
			return nil, errors.New("OSM matcher candidate query returned an invalid observation index")
		}
		counts[candidate.ObservationIndex]++
		if counts[candidate.ObservationIndex] > maxPerObservation {
			index := candidate.ObservationIndex
			return nil, &MatcherOverflowError{Operation: "candidates", Limit: maxPerObservation, ObservationIndex: &index}
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read OSM matcher candidates: %w", err)
	}
	return result, nil
}

const matcherIncidentEdgesSQL = `
WITH requested_nodes AS (
    SELECT node_id,ordinal::integer FROM unnest($1::uuid[]) WITH ORDINALITY input(node_id,ordinal)
), incident_key AS (
    SELECT node.ordinal,node.node_id AS requested_node_id,
           segment.region_id,segment.generation_id,segment.segment_id
    FROM requested_nodes node
    JOIN osm_canonical.path_segments segment ON segment.start_graph_node_id=node.node_id
    UNION
    SELECT node.ordinal,node.node_id AS requested_node_id,
           segment.region_id,segment.generation_id,segment.segment_id
    FROM requested_nodes node
    JOIN osm_canonical.path_segments segment ON segment.end_graph_node_id=node.node_id
)
SELECT key.requested_node_id,segment.segment_id,segment.region_id,segment.generation_id,
       segment.source_way_id,segment.source_way_version,segment.derivation_version,
       segment.start_graph_node_id,segment.end_graph_node_id,segment.logical_path_id,
       segment.locality_relation_id,segment.name,segment.normalized_name,segment.highway,
       segment.broad_class,segment.tags,segment.motor_forward_allowed,
       segment.motor_reverse_allowed,segment.length_m,
       ST_X(ST_StartPoint(segment.geom)),ST_Y(ST_StartPoint(segment.geom)),
       ST_X(ST_EndPoint(segment.geom)),ST_Y(ST_EndPoint(segment.geom))
FROM incident_key key
JOIN osm_canonical.path_segments segment
  ON segment.region_id=key.region_id AND segment.generation_id=key.generation_id
 AND segment.segment_id=key.segment_id
WHERE NOT EXISTS (
    SELECT 1 FROM osm_canonical.ways preferred_way
    WHERE preferred_way.way_id=segment.source_way_id
      AND (preferred_way.version,preferred_way.generation_id,preferred_way.region_id)
          > (segment.source_way_version,segment.generation_id,segment.region_id)
)
AND NOT EXISTS (
    SELECT 1 FROM osm_canonical.path_segments preferred_segment
    WHERE preferred_segment.segment_id=segment.segment_id
      AND (preferred_segment.generation_id,preferred_segment.region_id)
          > (segment.generation_id,segment.region_id)
)
ORDER BY key.ordinal,segment.segment_id,segment.generation_id DESC,segment.region_id DESC
LIMIT $2`

func (s *MatcherSnapshot) IncidentEdges(ctx context.Context, nodeIDs []uuid.UUID, maxEdges int) ([]MatcherIncidentEdge, error) {
	if len(nodeIDs) == 0 || len(nodeIDs) > maxIncidentNodes {
		return nil, &MatcherBoundsError{Field: "incident node count", Limit: maxIncidentNodes}
	}
	if maxEdges < 1 || maxEdges > maxIncidentEdges {
		return nil, &MatcherBoundsError{Field: "incident edge limit", Limit: maxIncidentEdges}
	}
	for _, nodeID := range nodeIDs {
		if nodeID == uuid.Nil {
			return nil, &MatcherBoundsError{Field: "incident node IDs", Limit: maxIncidentNodes}
		}
	}
	if err := s.queryable(); err != nil {
		return nil, err
	}
	rows, err := s.tx.Query(ctx, matcherIncidentEdgesSQL, nodeIDs, maxEdges+1)
	if err != nil {
		return nil, fmt.Errorf("query OSM matcher incident edges: %w", err)
	}
	defer rows.Close()
	result := make([]MatcherIncidentEdge, 0, maxEdges)
	for rows.Next() {
		if len(result) == maxEdges {
			return nil, &MatcherOverflowError{Operation: "incident edges", Limit: maxEdges}
		}
		var edge MatcherIncidentEdge
		if err := rows.Scan(
			&edge.RequestedNodeID, &edge.SegmentID, &edge.RegionID, &edge.GenerationID,
			&edge.SourceWayID, &edge.SourceWayVersion, &edge.DerivationVersion,
			&edge.StartGraphNodeID, &edge.EndGraphNodeID, &edge.LogicalPathID,
			&edge.LocalityRelationID, &edge.Name, &edge.NormalizedName,
			&edge.Highway, &edge.BroadClass, &edge.Tags, &edge.MotorForwardAllowed,
			&edge.MotorReverseAllowed, &edge.LengthMeters,
			&edge.StartLongitude, &edge.StartLatitude, &edge.EndLongitude, &edge.EndLatitude,
		); err != nil {
			return nil, fmt.Errorf("scan OSM matcher incident edge: %w", err)
		}
		result = append(result, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read OSM matcher incident edges: %w", err)
	}
	return result, nil
}

const matcherClipPortionsSQL = `
WITH requested AS (
    SELECT ordinal::integer-1 AS ordinal,region_id,generation_id,segment_id,
           source_from_fraction,source_to_fraction,forward
    FROM unnest($1::text[],$2::bigint[],$3::uuid[],$4::double precision[],
                $5::double precision[],$6::boolean[])
         WITH ORDINALITY AS input(region_id,generation_id,segment_id,
                                  source_from_fraction,source_to_fraction,forward,ordinal)
), clipped AS (
    SELECT requested.ordinal,segment.segment_id,segment.region_id,segment.generation_id,
           segment.derivation_version,segment.logical_path_id,segment.locality_relation_id,
           segment.source_way_id,segment.source_way_version,requested.forward,
           requested.source_from_fraction,requested.source_to_fraction,
           CASE WHEN requested.forward THEN
               ST_LineSubstring(segment.geom,requested.source_from_fraction,requested.source_to_fraction)
           ELSE ST_Reverse(
               ST_LineSubstring(segment.geom,requested.source_from_fraction,requested.source_to_fraction)
           ) END AS geom
    FROM requested
    JOIN osm_canonical.path_segments segment
      ON segment.region_id=requested.region_id
     AND segment.generation_id=requested.generation_id
     AND segment.segment_id=requested.segment_id
)
SELECT ordinal,segment_id,region_id,generation_id,derivation_version,logical_path_id,
       locality_relation_id,source_way_id,source_way_version,forward,
       source_from_fraction,source_to_fraction,ST_AsGeoJSON(geom)::jsonb,
       ST_Length(geom::geography)
FROM clipped
ORDER BY ordinal
LIMIT $7`

// ClipPortions returns exact geometry from the snapshot without persisting it.
func (s *MatcherSnapshot) ClipPortions(ctx context.Context, refs []MatcherPortionRef) ([]MatcherClippedPortion, error) {
	if len(refs) == 0 || len(refs) > maxClipPortionRefs {
		return nil, &MatcherBoundsError{Field: "clip portion reference count", Limit: maxClipPortionRefs}
	}
	regions := make([]string, len(refs))
	generations := make([]int64, len(refs))
	segments := make([]uuid.UUID, len(refs))
	fromFractions := make([]float64, len(refs))
	toFractions := make([]float64, len(refs))
	forward := make([]bool, len(refs))
	seenRefs := make(map[MatcherPortionRef]struct{}, len(refs))
	for i, ref := range refs {
		if ref.SegmentID == uuid.Nil || ref.RegionID == "" || ref.GenerationID <= 0 {
			return nil, &MatcherBoundsError{Field: "clip portion provenance", Limit: maxClipPortionRefs}
		}
		if !finite(ref.SourceFromFraction) || !finite(ref.SourceToFraction) ||
			ref.SourceFromFraction < 0 || ref.SourceToFraction > 1 ||
			ref.SourceFromFraction >= ref.SourceToFraction {
			return nil, &MatcherBoundsError{Field: "clip portion fractions", Limit: maxClipPortionRefs}
		}
		if ref.Direction != MatcherForward && ref.Direction != MatcherReverse {
			return nil, &MatcherBoundsError{Field: "clip portion direction", Limit: maxClipPortionRefs}
		}
		if _, duplicate := seenRefs[ref]; duplicate {
			return nil, &MatcherBoundsError{Field: "unique clip portion references", Limit: maxClipPortionRefs}
		}
		seenRefs[ref] = struct{}{}
		regions[i], generations[i], segments[i] = ref.RegionID, ref.GenerationID, ref.SegmentID
		fromFractions[i], toFractions[i] = ref.SourceFromFraction, ref.SourceToFraction
		forward[i] = ref.Direction == MatcherForward
	}
	if err := s.queryable(); err != nil {
		return nil, err
	}
	rows, err := s.tx.Query(ctx, matcherClipPortionsSQL, regions, generations, segments,
		fromFractions, toFractions, forward, len(refs)+1)
	if err != nil {
		return nil, fmt.Errorf("query OSM matcher clipped portions: %w", err)
	}
	defer rows.Close()
	result := make([]MatcherClippedPortion, 0, len(refs))
	seenOrdinals := make([]bool, len(refs))
	for rows.Next() {
		if len(result) == len(refs) {
			return nil, &MatcherOverflowError{Operation: "clipped portions", Limit: maxClipPortionRefs}
		}
		var portion MatcherClippedPortion
		var isForward bool
		if err := rows.Scan(
			&portion.Ordinal, &portion.SegmentID, &portion.RegionID, &portion.GenerationID,
			&portion.DerivationVersion, &portion.LogicalPathID, &portion.LocalityRelationID,
			&portion.SourceWayID, &portion.SourceWayVersion, &isForward,
			&portion.SourceFromFraction, &portion.SourceToFraction, &portion.GeoJSON,
			&portion.LengthMeters,
		); err != nil {
			return nil, fmt.Errorf("scan OSM matcher clipped portion: %w", err)
		}
		if portion.Ordinal < 0 || portion.Ordinal >= len(refs) || seenOrdinals[portion.Ordinal] {
			return nil, errors.New("OSM matcher clipped portion query returned a duplicate or invalid reference")
		}
		seenOrdinals[portion.Ordinal] = true
		portion.Direction = MatcherReverse
		if isForward {
			portion.Direction = MatcherForward
		}
		result = append(result, portion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read OSM matcher clipped portions: %w", err)
	}
	if len(result) != len(refs) {
		return nil, errors.New("OSM matcher clipped portion query returned an absent reference")
	}
	return result, nil
}

func (s *MatcherSnapshot) Generations(ctx context.Context) ([]MatcherGeneration, error) {
	if err := s.queryable(); err != nil {
		return nil, err
	}
	rows, err := s.tx.Query(ctx, `
		SELECT region_id,id,source_url,source_sha256,source_header_timestamp,
		       importer_version,derivation_version,promoted_at
		FROM osm_catalog.generations WHERE state='active' ORDER BY region_id,id`)
	if err != nil {
		return nil, fmt.Errorf("query OSM matcher generations: %w", err)
	}
	defer rows.Close()
	var result []MatcherGeneration
	for rows.Next() {
		var generation MatcherGeneration
		var sourceSHA *string
		if err := rows.Scan(
			&generation.RegionID, &generation.GenerationID, &generation.SourceURL,
			&sourceSHA, &generation.SourceHeaderTimestamp,
			&generation.ImporterVersion, &generation.DerivationVersion, &generation.PromotedAt,
		); err != nil {
			return nil, fmt.Errorf("scan OSM matcher generation: %w", err)
		}
		if sourceSHA == nil {
			return nil, errors.New("OSM matcher generation source digest is unavailable")
		}
		generation.SourceSHA256, err = hex.DecodeString(*sourceSHA)
		if err != nil || len(generation.SourceSHA256) != 32 {
			return nil, errors.New("OSM matcher generation source digest is invalid")
		}
		result = append(result, generation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read OSM matcher generations: %w", err)
	}
	return result, nil
}

func (s *MatcherSnapshot) UnavailableRegions(ctx context.Context, observations []MatcherObservation) ([]MatcherUnavailableRegion, error) {
	if err := s.queryable(); err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return []MatcherUnavailableRegion{}, nil
	}
	longitudes, latitudes := make([]float64, len(observations)), make([]float64, len(observations))
	for i, observation := range observations {
		longitudes[i], latitudes[i] = observation.Longitude, observation.Latitude
	}
	rows, err := s.tx.Query(ctx, `
		WITH route_point AS (
			SELECT ST_SetSRID(ST_MakePoint(longitude,latitude),4326) AS location
			FROM unnest($1::double precision[],$2::double precision[]) point(longitude,latitude)
		), unavailable AS (
			SELECT catalog.region_id,catalog.display_name
			FROM route_point point
			LEFT JOIN LATERAL (
				SELECT region.id AS region_id
				FROM osm_catalog.regions region
				JOIN osm_catalog.generations generation ON generation.region_id=region.id AND generation.state='active'
				WHERE region.configured AND ST_Covers(region.boundary,point.location)
				ORDER BY ST_Area(region.boundary::geography),region.id LIMIT 1
			) active ON true
			JOIN LATERAL (
				SELECT region.id AS region_id,region.display_name
				FROM osm_catalog.regions region
				WHERE ST_Covers(region.boundary,point.location)
				ORDER BY ST_Area(region.boundary::geography),region.id LIMIT 1
			) catalog ON active.region_id IS NULL
		)
		SELECT region_id,display_name FROM unavailable GROUP BY region_id,display_name ORDER BY display_name,region_id`, longitudes, latitudes)
	if err != nil {
		return nil, fmt.Errorf("resolve unavailable OSM matcher regions: %w", err)
	}
	defer rows.Close()
	regions := []MatcherUnavailableRegion{}
	for rows.Next() {
		var region MatcherUnavailableRegion
		if err := rows.Scan(&region.RegionID, &region.DisplayName); err != nil {
			return nil, fmt.Errorf("read unavailable OSM matcher region: %w", err)
		}
		regions = append(regions, region)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read unavailable OSM matcher regions: %w", err)
	}
	return regions, nil
}

func (s *MatcherSnapshot) queryable() error {
	if s == nil || s.tx == nil || s.closed {
		return pgx.ErrTxClosed
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
