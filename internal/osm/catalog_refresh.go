package osm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type CatalogProgress struct {
	Phase              string `json:"phase"`
	Provider           string `json:"provider"`
	Total              int    `json:"total"`
	Completed          int    `json:"completed"`
	Failed             int    `json:"failed"`
	ProvidersCompleted int    `json:"providersCompleted"`
	ProvidersFailed    int    `json:"providersFailed"`
}

type CatalogRefreshJob struct {
	ID          string          `json:"id"`
	State       string          `json:"state"`
	Providers   []string        `json:"providers"`
	Progress    CatalogProgress `json:"progress"`
	CreatedAt   time.Time       `json:"createdAt"`
	StartedAt   *time.Time      `json:"startedAt"`
	FinishedAt  *time.Time      `json:"finishedAt"`
	FailureCode *string         `json:"failureCode"`
}

type ProviderCatalog struct {
	Provider       string     `json:"provider"`
	State          string     `json:"state"`
	RefreshedAt    *time.Time `json:"refreshedAt"`
	AttemptedAt    *time.Time `json:"attemptedAt"`
	Regions        int        `json:"regions"`
	MetadataFailed int        `json:"metadataFailed"`
	FailureCode    *string    `json:"failureCode"`
}

type CatalogRegion struct {
	ID                       string           `json:"id"`
	Provider                 string           `json:"provider"`
	ProviderID               string           `json:"providerId"`
	ParentID                 *string          `json:"parentId"`
	Name                     string           `json:"name"`
	Available                bool             `json:"available"`
	AreaM2                   float64          `json:"areaM2"`
	PBFBytes                 *int64           `json:"pbfBytes"`
	PBFModifiedAt            *time.Time       `json:"pbfModifiedAt"`
	MetadataCheckedAt        *time.Time       `json:"metadataCheckedAt"`
	MetadataAttemptedAt      *time.Time       `json:"metadataAttemptedAt"`
	MetadataErrorCode        *string          `json:"metadataErrorCode"`
	Generation               *int64           `json:"generation"`
	BuildID                  *int64           `json:"buildId"`
	PromotionNumbers         map[string]int64 `json:"promotionNumbers,omitempty"`
	DownloadedAt             *time.Time       `json:"downloadedAt"`
	DownloadedBytes          *int64           `json:"downloadedBytes"`
	SourceTimestamp          *time.Time       `json:"sourceTimestamp"`
	PromotedAt               *time.Time       `json:"promotedAt"`
	GenerationElapsedSeconds *float64         `json:"generationElapsedSeconds,omitempty"`
	DatabaseBytes            *int64           `json:"databaseBytes"`
	DatabaseMeasuredAt       *time.Time       `json:"databaseMeasuredAt"`
	BuildDatabaseBytes       *int64           `json:"buildDatabaseBytes"`
	BuildDatabaseMeasuredAt  *time.Time       `json:"buildDatabaseMeasuredAt"`
	BuildDatabaseID          *int64           `json:"buildDatabaseId"`
	ProcessedDatabaseBytes   *int64           `json:"processedDatabaseBytes"`
	Boundary                 json.RawMessage  `json:"boundary,omitempty"`
	Update                   *RegionUpdate    `json:"update,omitempty"`
	Status                   string           `json:"status,omitempty"`
	ReprocessingRequired     bool             `json:"reprocessingRequired"`
	WorkoutCount             int64            `json:"workoutCount"`
	CoverageRouteCount       int64            `json:"coverageRouteCount"`
	MissingRegionRouteCount  int64            `json:"missingRegionRouteCount"`
}

const CatalogJobJSON = `jsonb_build_object('id',upper(replace(id::text,'-','')),'state',state,'providers',providers,
    'progress',progress,'createdAt',created_at,'startedAt',started_at,'finishedAt',finished_at,'failureCode',failure_code)`

const regionalGenerationSQL = `g.promotion_number`

func CatalogRegions(ctx context.Context, pool *pgxpool.Pool, provider string) ([]CatalogRegion, error) {
	rows, err := pool.Query(ctx, `SELECT jsonb_build_object(
        'id',r.id,'provider',r.provider,'providerId',COALESCE(r.provider_catalog_id,r.provider_region_id),
        'parentId',r.provider_parent_id,'name',r.display_name,'available',r.catalog_available,
        'areaM2',r.geographic_area_m2,'pbfBytes',r.advertised_download_bytes,'pbfModifiedAt',r.source_last_modified,
        'metadataCheckedAt',r.metadata_checked_at,'metadataAttemptedAt',r.metadata_attempted_at,'metadataErrorCode',r.metadata_error_code,
        'buildId',g.id,'generation',CASE WHEN g.id IS NOT NULL THEN `+regionalGenerationSQL+` END,
        'importerVersion',g.importer_version,'derivationVersion',g.derivation_version,
        'promotionNumbers',(SELECT COALESCE(jsonb_object_agg(history.id::text,history.promotion_number),'{}'::jsonb)
            FROM osm_catalog.generations history WHERE history.region_id=r.id AND history.promoted_at IS NOT NULL
            AND history.promotion_number IS NOT NULL),
        'downloadedAt',g.downloaded_at,'downloadedBytes',g.downloaded_bytes,'sourceTimestamp',g.source_header_timestamp,
        'promotedAt',g.promoted_at,'processedDatabaseBytes',g.processed_database_bytes,
        'databaseBytes',CASE WHEN g.id IS NOT NULL THEN osm_catalog.region_database_bytes(r.id) END,
        'databaseMeasuredAt',CASE WHEN g.id IS NOT NULL THEN clock_timestamp() END,
        'buildDatabaseBytes',candidate.build_database_bytes,'buildDatabaseMeasuredAt',candidate.build_database_measured_at,
        'buildDatabaseId',candidate.id,
        'boundary',ST_AsGeoJSON(r.boundary)::jsonb)
        FROM osm_catalog.regions r LEFT JOIN osm_catalog.generations g ON g.region_id=r.id AND g.state='active'
        LEFT JOIN LATERAL (SELECT id,build_database_bytes,build_database_measured_at FROM osm_catalog.generations b
            WHERE b.region_id=r.id AND b.promoted_at IS NULL AND b.build_database_bytes IS NOT NULL
                AND to_regnamespace(b.schema_name::text) IS NOT NULL ORDER BY id DESC LIMIT 1) candidate ON true
        WHERE r.provider=$1 ORDER BY lower(r.display_name),r.id`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CatalogRegion{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var region CatalogRegion
		wire := struct {
			*CatalogRegion
			ImporterVersion   int `json:"importerVersion"`
			DerivationVersion int `json:"derivationVersion"`
		}{CatalogRegion: &region}
		if err = json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}
		region.Name = DisplayRegionName(region.Name)
		region.Status = RegionProcessingStatus(region.Generation != nil, wire.ImporterVersion, wire.DerivationVersion)
		region.ReprocessingRequired = region.Status == "Stale"
		result = append(result, region)
	}
	return result, rows.Err()
}

func RegionProcessingStatus(promoted bool, importerVersion, derivationVersion int) string {
	if !promoted {
		return "Available"
	}
	if importerVersion < ImporterVersion || derivationVersion < DerivationVersion {
		return "Stale"
	}
	return "Ready"
}
