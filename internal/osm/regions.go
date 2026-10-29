package osm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RouteCoordinate struct {
	Longitude float64
	Latitude  float64
}

type RegionGeneration struct {
	RegionID   string `json:"regionId"`
	Generation int64  `json:"generation"`
}

type ConfiguredRegion struct {
	RegionID    string `json:"regionId"`
	DisplayName string `json:"displayName"`
}

type RouteRegionReadiness struct {
	State                string
	Reason               string
	Regions              []RegionGeneration
	CatalogFingerprint   string
	CatalogRevision      int64
	SelectionFingerprint string
}

type RouteRegionCatalog struct {
	Revision             int64
	Fingerprint          string
	SelectionFingerprint string
	PromotionEventID     int64
	ActiveGenerations    map[string]int64
	ConfiguredRegions    []ConfiguredRegion
}

func ConfiguredRegions(ctx context.Context, pool *pgxpool.Pool) ([]ConfiguredRegion, error) {
	if pool == nil {
		return nil, errors.New("OSM database is unavailable")
	}
	rows, err := pool.Query(ctx, `SELECT id,display_name FROM osm_catalog.regions WHERE configured ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read configured OSM regions: %w", err)
	}
	defer rows.Close()
	regions := make([]ConfiguredRegion, 0)
	for rows.Next() {
		var region ConfiguredRegion
		if err := rows.Scan(&region.RegionID, &region.DisplayName); err != nil {
			return nil, fmt.Errorf("read configured OSM region: %w", err)
		}
		regions = append(regions, region)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read configured OSM regions: %w", err)
	}
	return regions, nil
}

func ActiveRegionGenerations(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, error) {
	rows, err := pool.Query(ctx, `SELECT region_id,id FROM osm_catalog.generations WHERE state='active' ORDER BY region_id`)
	if err != nil {
		return nil, fmt.Errorf("read active OSM region generations: %w", err)
	}
	defer rows.Close()
	result := make(map[string]int64)
	for rows.Next() {
		var regionID string
		var generation int64
		if err := rows.Scan(&regionID, &generation); err != nil {
			return nil, fmt.Errorf("read active OSM region generation: %w", err)
		}
		result[regionID] = generation
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active OSM region generations: %w", err)
	}
	return result, nil
}

type pointRegionObservation struct {
	regionID   *string
	generation *int64
	inCatalog  bool
}

// ResolveRouteRegions queries only the local catalog. No route coordinates are
// persisted in, or sent beyond, the OSM database.
func ResolveRouteRegions(ctx context.Context, pool *pgxpool.Pool, points []RouteCoordinate) (RouteRegionReadiness, error) {
	if pool == nil {
		return RouteRegionReadiness{}, errors.New("OSM database is unavailable")
	}
	return resolveRouteRegions(ctx, pool, points)
}

// ResolveRouteRegionsAtPromotion fences the lookup against an already observed
// promotion. The head and geometry share one physical connection and snapshot;
// no older replica result is returned, even for an empty route.
func ResolveRouteRegionsAtPromotion(ctx context.Context, pool *pgxpool.Pool, points []RouteCoordinate, minimumEventID int64) (RouteRegionReadiness, error) {
	// Reconciliation heartbeats run between routes, so do not wait indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshot, err := BeginMatcherSnapshot(ctx, pool)
	if err != nil {
		return RouteRegionReadiness{}, err
	}
	defer closeMatcherSnapshot(ctx, snapshot)
	var eventID int64
	if err := snapshot.tx.QueryRow(ctx, `SELECT osm_catalog.promotion_event_head()`).Scan(&eventID); err != nil {
		return RouteRegionReadiness{}, fmt.Errorf("read OSM promotion event head: %w", err)
	}
	if eventID < minimumEventID {
		// Close while held so the pool cannot keep returning this lagged node.
		discardReplicaSnapshot(ctx, snapshot.tx)
		return RouteRegionReadiness{}, ErrMatcherReplicaNotReady
	}
	return resolveRouteRegions(ctx, snapshot.tx, points)
}

// ResolveRouteRegionsWithCatalog binds ROI to the complete selection catalog,
// including competing regions that were not chosen for this route. Both reads
// share one read-only repeatable-read snapshot; no geometry leaves the catalog.
func ResolveRouteRegionsWithCatalog(ctx context.Context, pool *pgxpool.Pool, points []RouteCoordinate, minimumEventID, minimumCatalogRevision int64) (RouteRegionReadiness, error) {
	return ResolveRouteRegionsWithCatalogBudget(ctx, pool, points, minimumEventID, minimumCatalogRevision, 0)
}

// RouteRegionResolutionError identifies only the point-query stage. Connection,
// catalog-head and replica-fence failures are never tagged as route-local.
type RouteRegionResolutionError struct {
	Err              error
	DeadlineExceeded bool
	InvalidData      bool
}

func (err *RouteRegionResolutionError) Error() string { return err.Err.Error() }
func (err *RouteRegionResolutionError) Unwrap() error { return err.Err }

var ErrRouteRegionResultInvalid = errors.New("OSM provider region query returned incomplete results")

// The budget starts after the authorized catalog snapshot/freshness read. It
// bounds actual ROI only, not APP authority or a whole reconciliation campaign.
func ResolveRouteRegionsWithCatalogBudget(ctx context.Context, pool *pgxpool.Pool, points []RouteCoordinate, minimumEventID, minimumCatalogRevision int64, roiBudget time.Duration) (RouteRegionReadiness, error) {
	snapshot, err := BeginMatcherSnapshot(ctx, pool)
	if err != nil {
		return RouteRegionReadiness{}, err
	}
	defer closeMatcherSnapshot(ctx, snapshot)
	catalog, err := readRouteRegionCatalog(ctx, snapshot.tx, minimumEventID, minimumCatalogRevision)
	if err != nil {
		return RouteRegionReadiness{}, err
	}
	roiCtx := ctx
	if roiBudget > 0 {
		var cancel context.CancelFunc
		roiCtx, cancel = context.WithTimeout(ctx, roiBudget)
		defer cancel()
	}
	readiness, err := resolveRouteRegions(roiCtx, snapshot.tx, points)
	if err != nil {
		var row pgx.ScanArgError
		var sql *pgconn.PgError
		invalid := errors.Is(err, ErrRouteRegionResultInvalid) || errors.As(err, &row)
		if errors.As(err, &sql) {
			invalid = sql.Code == "22003" || sql.Code == "22P02" || sql.Code == "22023"
		}
		return RouteRegionReadiness{}, &RouteRegionResolutionError{Err: err,
			DeadlineExceeded: roiBudget > 0 && roiCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil,
			InvalidData:      invalid}
	}
	readiness.CatalogFingerprint = catalog.Fingerprint
	readiness.CatalogRevision = catalog.Revision
	readiness.SelectionFingerprint = catalog.SelectionFingerprint
	return readiness, nil
}

// ReadRouteRegionCatalog observes the full selection proof and active heads in
// the same snapshot used to fence replica lag. Only hashes and small head metadata
// cross the connection, never provider boundary coordinates.
func ReadRouteRegionCatalog(ctx context.Context, pool *pgxpool.Pool, minimumEventID, minimumCatalogRevision int64) (RouteRegionCatalog, error) {
	snapshot, err := BeginMatcherSnapshot(ctx, pool)
	if err != nil {
		return RouteRegionCatalog{}, err
	}
	defer closeMatcherSnapshot(ctx, snapshot)
	return readRouteRegionCatalog(ctx, snapshot.tx, minimumEventID, minimumCatalogRevision)
}

func readRouteRegionCatalog(ctx context.Context, tx pgx.Tx, minimumEventID, minimumCatalogRevision int64) (RouteRegionCatalog, error) {
	var catalog RouteRegionCatalog
	var fingerprint, selection, heads, configured []byte
	if err := tx.QueryRow(ctx, readRouteRegionCatalogSQL).Scan(&catalog.Revision, &fingerprint,
		&selection, &heads, &configured, &catalog.PromotionEventID); err != nil {
		return RouteRegionCatalog{}, fmt.Errorf("read OSM route region catalog proof: %w", err)
	}
	if catalog.Revision < 1 || len(fingerprint) != 32 || len(selection) != 32 || catalog.PromotionEventID < 0 {
		return RouteRegionCatalog{}, errors.New("invalid OSM route region catalog proof")
	}
	if catalog.Revision < minimumCatalogRevision || catalog.PromotionEventID < minimumEventID {
		discardReplicaSnapshot(ctx, tx)
		return RouteRegionCatalog{}, ErrMatcherReplicaNotReady
	}
	catalog.Fingerprint = hex.EncodeToString(fingerprint)
	catalog.SelectionFingerprint = hex.EncodeToString(selection)
	if err := json.Unmarshal(heads, &catalog.ActiveGenerations); err != nil {
		return RouteRegionCatalog{}, fmt.Errorf("decode OSM route region catalog heads: %w", err)
	}
	if err := json.Unmarshal(configured, &catalog.ConfiguredRegions); err != nil {
		return RouteRegionCatalog{}, fmt.Errorf("decode configured OSM route regions: %w", err)
	}
	if catalog.ActiveGenerations == nil || catalog.ConfiguredRegions == nil {
		return RouteRegionCatalog{}, errors.New("missing OSM route region catalog metadata")
	}
	return catalog, nil
}

const readRouteRegionCatalogSQL = `SELECT revision,fingerprint,selection_fingerprint,
	active_generations,configured_regions,osm_catalog.promotion_event_head()
	FROM osm_catalog.route_region_catalog_state WHERE singleton`

type regionQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func resolveRouteRegions(ctx context.Context, db regionQuerier, points []RouteCoordinate) (RouteRegionReadiness, error) {
	if len(points) == 0 {
		return RouteRegionReadiness{State: "unavailable", Reason: "no_provider_region", Regions: []RegionGeneration{}}, nil
	}
	longitudes, latitudes := make([]float64, len(points)), make([]float64, len(points))
	for i, point := range points {
		longitudes[i], latitudes[i] = point.Longitude, point.Latitude
	}
	rows, err := db.Query(ctx, resolveRouteRegionsSQL, longitudes, latitudes)
	if err != nil {
		return RouteRegionReadiness{}, fmt.Errorf("resolve OSM provider regions: %w", err)
	}
	defer rows.Close()
	observations := make([]pointRegionObservation, 0, len(points))
	for rows.Next() {
		var ordinal int
		var observation pointRegionObservation
		if err := rows.Scan(&ordinal, &observation.regionID, &observation.generation, &observation.inCatalog); err != nil {
			return RouteRegionReadiness{}, fmt.Errorf("read OSM provider region: %w", err)
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return RouteRegionReadiness{}, fmt.Errorf("read OSM provider regions: %w", err)
	}
	if len(observations) != len(points) {
		return RouteRegionReadiness{}, ErrRouteRegionResultInvalid
	}
	return readinessFromObservations(observations), nil
}

const resolveRouteRegionsSQL = `
		WITH route_point AS (
			SELECT ordinal::integer AS ordinal,ST_SetSRID(ST_MakePoint(longitude,latitude),4326) AS location
			FROM unnest($1::double precision[],$2::double precision[]) WITH ORDINALITY point(longitude,latitude,ordinal)
		)
		SELECT point.ordinal,active.region_id,active.generation_id,(catalog.region_id IS NOT NULL)
		FROM route_point point
		LEFT JOIN LATERAL (
			SELECT region.id AS region_id,generation.id AS generation_id
			FROM osm_catalog.regions region
			JOIN osm_catalog.generations generation ON generation.region_id=region.id AND generation.state='active'
			WHERE region.configured AND ST_Covers(region.boundary,point.location)
			ORDER BY region.geographic_area_m2,region.id LIMIT 1
		) active ON true
		LEFT JOIN LATERAL (
			SELECT region.id AS region_id FROM osm_catalog.regions region
			WHERE ST_Covers(region.boundary,point.location)
			ORDER BY region.geographic_area_m2,region.id LIMIT 1
		) catalog ON true
		ORDER BY point.ordinal`

func readinessFromObservations(observations []pointRegionObservation) RouteRegionReadiness {
	regions := make(map[string]int64)
	for _, observation := range observations {
		if !observation.inCatalog {
			return RouteRegionReadiness{State: "unavailable", Reason: "no_provider_region", Regions: []RegionGeneration{}}
		}
		if observation.regionID == nil || observation.generation == nil {
			return RouteRegionReadiness{State: "pending", Reason: "region_not_active", Regions: []RegionGeneration{}}
		}
		regions[*observation.regionID] = *observation.generation
	}
	result := RouteRegionReadiness{State: "map_data_ready", Regions: make([]RegionGeneration, 0, len(regions))}
	for regionID, generation := range regions {
		result.Regions = append(result.Regions, RegionGeneration{RegionID: regionID, Generation: generation})
	}
	sort.Slice(result.Regions, func(i, j int) bool { return result.Regions[i].RegionID < result.Regions[j].RegionID })
	return result
}
