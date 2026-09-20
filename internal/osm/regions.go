package osm

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
	State   string
	Reason  string
	Regions []RegionGeneration
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
	if len(points) == 0 {
		return RouteRegionReadiness{State: "unavailable", Reason: "no_provider_region", Regions: []RegionGeneration{}}, nil
	}
	longitudes, latitudes := make([]float64, len(points)), make([]float64, len(points))
	for i, point := range points {
		longitudes[i], latitudes[i] = point.Longitude, point.Latitude
	}
	rows, err := pool.Query(ctx, `
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
			ORDER BY ST_Area(region.boundary::geography),region.id LIMIT 1
		) active ON true
		LEFT JOIN LATERAL (
			SELECT region.id AS region_id FROM osm_catalog.regions region
			WHERE ST_Covers(region.boundary,point.location)
			ORDER BY ST_Area(region.boundary::geography),region.id LIMIT 1
		) catalog ON true
		ORDER BY point.ordinal`, longitudes, latitudes)
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
		return RouteRegionReadiness{}, errors.New("OSM provider region query returned incomplete results")
	}
	return readinessFromObservations(observations), nil
}

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
