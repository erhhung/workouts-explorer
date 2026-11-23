package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CatalogRefreshRunner struct {
	db, catalog *pgxpool.Pool
	providers   []string
	concurrency int
	logger      *slog.Logger
	HTTPClient  *http.Client
}

func NewCatalogRefreshRunner(db, catalog *pgxpool.Pool, providers []string, concurrency int, logger *slog.Logger) *CatalogRefreshRunner {
	return &CatalogRefreshRunner{db: db, catalog: catalog, providers: providers, concurrency: concurrency, logger: logger,
		HTTPClient: &http.Client{Timeout: 30 * time.Second}}
}

func (r *CatalogRefreshRunner) Run(ctx context.Context) error {
	nextProjection := time.Time{}
	for ctx.Err() == nil {
		if time.Now().After(nextProjection) {
			for _, provider := range r.providers {
				if err := r.project(ctx, provider); err != nil {
					r.logger.WarnContext(ctx, "OSM catalog projection deferred", "code", "catalog-projection-unavailable")
				}
			}
			nextProjection = time.Now().Add(5 * time.Minute)
		}
		worked, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.logger.WarnContext(ctx, "OSM catalog refresh deferred", "code", "catalog-refresh-unavailable")
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func (r *CatalogRefreshRunner) RunOnce(ctx context.Context) (bool, error) {
	if r.concurrency < 1 || r.concurrency > 32 {
		return false, errors.New("invalid catalog metadata concurrency")
	}
	_, err := r.db.Exec(ctx, `UPDATE app.osm_catalog_refresh_jobs SET state='failed',finished_at=clock_timestamp(),
        failure_code='catalog-worker-interrupted',lease_token=NULL,lease_expires_at=NULL
        WHERE state='running' AND lease_expires_at<=clock_timestamp() AND attempt>=3`)
	if err != nil {
		return false, err
	}
	lease := uuid.New()
	var id uuid.UUID
	var providers []string
	err = r.db.QueryRow(ctx, `WITH candidate AS (
        SELECT id FROM app.osm_catalog_refresh_jobs WHERE attempt<3 AND
        (state='queued' OR state='running' AND lease_expires_at<=clock_timestamp())
        ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
        UPDATE app.osm_catalog_refresh_jobs job SET state='running',attempt=attempt+1,
        started_at=COALESCE(started_at,clock_timestamp()),finished_at=NULL,failure_code=NULL,
        lease_token=$1,lease_expires_at=clock_timestamp()+interval '2 minutes',progress='{}'::jsonb
        FROM candidate WHERE job.id=candidate.id RETURNING job.id,job.providers`, lease).Scan(&id, &providers)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				command, err := r.db.Exec(work, `UPDATE app.osm_catalog_refresh_jobs SET lease_expires_at=clock_timestamp()+interval '2 minutes'
                WHERE id=$1 AND state='running' AND lease_token=$2 AND lease_expires_at>clock_timestamp()`, id, lease)
				if err != nil || command.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	progress := osm.CatalogProgress{Phase: "catalog"}
	err = r.refresh(work, id, lease, providers, &progress)
	cancel()
	<-heartbeatDone
	// Shutdown leaves the lease reclaimable instead of marking an interrupted
	// request successful. Completed metadata is safe to revisit on replacement.
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	state, code := "succeeded", ""
	if err != nil || progress.ProvidersCompleted == 0 {
		state, code = "failed", "catalog-refresh-unavailable"
	} else if progress.Failed > 0 || progress.ProvidersFailed > 0 {
		state, code = "partially_succeeded", "catalog-refresh-partial"
	}
	progress.Phase = "complete"
	raw, _ := json.Marshal(progress)
	var failure any
	if code != "" {
		failure = code
	}
	finish, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	command, finishErr := r.db.Exec(finish, `UPDATE app.osm_catalog_refresh_jobs SET state=$3,progress=$4::jsonb,
        failure_code=$5,finished_at=clock_timestamp(),lease_token=NULL,lease_expires_at=NULL
        WHERE id=$1 AND lease_token=$2 AND state='running' AND lease_expires_at>clock_timestamp()`, id, lease, state, raw, failure)
	if finishErr != nil {
		return true, finishErr
	}
	if command.RowsAffected() != 1 {
		return true, errors.New("catalog refresh lease lost")
	}
	r.logger.InfoContext(ctx, "OSM provider catalogs refreshed", "state", state, "regions", progress.Total, "metadata_failed", progress.Failed)
	return true, err
}

func (r *CatalogRefreshRunner) progress(ctx context.Context, id, lease uuid.UUID, progress *osm.CatalogProgress) error {
	raw, _ := json.Marshal(progress)
	command, err := r.db.Exec(ctx, `UPDATE app.osm_catalog_refresh_jobs SET progress=$3::jsonb
        WHERE id=$1 AND lease_token=$2 AND state='running' AND lease_expires_at>clock_timestamp()`, id, lease, raw)
	if err == nil && command.RowsAffected() != 1 {
		return errors.New("catalog refresh lease lost")
	}
	return err
}

func (r *CatalogRefreshRunner) refresh(ctx context.Context, id, lease uuid.UUID, providers []string, progress *osm.CatalogProgress) error {
	connection, err := r.catalog.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	var locked bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('workouts-osm-catalog-refresh',0))`).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("another catalog refresh owns the OSM database")
	}
	defer func() {
		unlock, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := connection.Exec(unlock, `SELECT pg_advisory_unlock(hashtextextended('workouts-osm-catalog-refresh',0))`); err != nil {
			_ = connection.Conn().Close(unlock)
		}
	}()
	for _, provider := range providers {
		progress.Phase, progress.Provider = "catalog", provider
		if err = r.progress(ctx, id, lease, progress); err != nil {
			return err
		}
		now := time.Now().UTC()
		status := osm.ProviderCatalog{Provider: provider, State: "failed", AttemptedAt: &now}
		var previous []byte
		if r.db.QueryRow(ctx, `SELECT payload FROM app.osm_provider_catalogs WHERE provider=$1`, provider).Scan(&previous) == nil {
			var old osm.ProviderCatalog
			if json.Unmarshal(previous, &old) == nil {
				status.RefreshedAt = old.RefreshedAt
				status.Regions = old.Regions
				status.MetadataFailed = old.MetadataFailed
			}
		}
		var regions []osm.Region
		var fetchErr error
		if provider != "geofabrik" || !slices.Contains(r.providers, provider) {
			fetchErr = errors.New("unsupported provider")
		} else {
			regions, fetchErr = osm.FetchGeofabrikCatalog(ctx, r.HTTPClient)
		}
		if fetchErr == nil {
			fetchErr = osm.SyncCatalog(ctx, r.catalog, regions)
		}
		if fetchErr != nil {
			progress.ProvidersFailed++
			code := "provider-catalog-unavailable"
			status.FailureCode = &code
			if err = r.saveProvider(ctx, status); err != nil {
				return err
			}
			continue
		}
		if err = r.project(ctx, provider); err != nil {
			return err
		}
		progress.Total += len(regions)
		progress.Phase = "metadata"
		if err = r.progress(ctx, id, lease, progress); err != nil {
			return err
		}
		failedBefore := progress.Failed
		progress.Phase = "metadata"
		if err = r.metadata(ctx, id, lease, regions, progress); err != nil {
			return err
		}
		progress.ProvidersCompleted++
		status.RefreshedAt = &now
		status.Regions = len(regions)
		status.MetadataFailed = progress.Failed - failedBefore
		status.State = "succeeded"
		status.FailureCode = nil
		if status.MetadataFailed > 0 {
			status.State = "partially_succeeded"
		}
		if err = r.project(ctx, provider); err != nil {
			return err
		}
		if err = r.saveProvider(ctx, status); err != nil {
			return err
		}
	}
	return nil
}

func (r *CatalogRefreshRunner) metadata(ctx context.Context, id, lease uuid.UUID, regions []osm.Region, progress *osm.CatalogProgress) error {
	type result struct {
		region   osm.Region
		metadata osm.RegionMetadata
		err      error
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	tasks := make(chan osm.Region)
	results := make(chan result, r.concurrency)
	var workers sync.WaitGroup
	for range r.concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for region := range tasks {
				var metadata osm.RegionMetadata
				var err error
				for attempt := range 2 {
					metadata, err = osm.FetchRegionMetadata(work, r.HTTPClient, region)
					if err == nil {
						break
					}
					if attempt == 0 {
						select {
						case <-work.Done():
							return
						case <-time.After(time.Second):
						}
					}
				}
				select {
				case results <- result{region, metadata, err}:
				case <-work.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(tasks)
		for _, region := range regions {
			select {
			case tasks <- region:
			case <-work.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	// One consumer persists results and progress; network concurrency never
	// multiplies PostgreSQL writers or exceeds the configured per-job ceiling.
	for result := range results {
		if err := r.progress(work, id, lease, progress); err != nil {
			cancel()
			return err
		}
		var err error
		if result.err != nil {
			progress.Failed++
			_, err = r.catalog.Exec(work, `UPDATE osm_catalog.regions SET metadata_attempted_at=clock_timestamp(),metadata_error_code='provider-metadata-unavailable' WHERE id=$1`, result.region.ID)
		} else {
			_, err = r.catalog.Exec(work, `UPDATE osm_catalog.regions SET advertised_download_bytes=$2,source_last_modified=$3,
                metadata_checked_at=$4,metadata_attempted_at=$4,metadata_error_code=NULL WHERE id=$1`, result.region.ID, result.metadata.Bytes, result.metadata.ModifiedAt, result.metadata.CheckedAt)
		}
		if err != nil {
			cancel()
			return err
		}
		progress.Completed++
		if err = r.progress(work, id, lease, progress); err != nil {
			cancel()
			return err
		}
		if progress.Completed%10 == 0 {
			if err = r.project(work, result.region.Provider); err != nil {
				cancel()
				return err
			}
		}
	}
	return work.Err()
}

func (r *CatalogRefreshRunner) saveProvider(ctx context.Context, provider osm.ProviderCatalog) error {
	raw, _ := json.Marshal(provider)
	_, err := r.db.Exec(ctx, `INSERT INTO app.osm_provider_catalogs(provider,payload) VALUES($1,$2::jsonb)
        ON CONFLICT(provider) DO UPDATE SET payload=EXCLUDED.payload`, provider.Provider, raw)
	return err
}

func (r *CatalogRefreshRunner) project(ctx context.Context, provider string) error {
	regions, err := osm.CatalogRegions(ctx, r.catalog, provider)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(regions)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `INSERT INTO app.osm_region_catalog(id,provider,payload)
        SELECT value->>'id',value->>'provider',value FROM jsonb_array_elements($1::jsonb)
        ON CONFLICT(id) DO UPDATE SET provider=EXCLUDED.provider,payload=EXCLUDED.payload`, raw)
	return err
}
