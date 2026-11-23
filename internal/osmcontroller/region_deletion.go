package osmcontroller

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/erhhung/workouts-explorer/internal/osm"
)

func (c *Controller) finishDeletion(ctx context.Context, e execution) error {
	complete, err := osm.RegionDeletionComplete(ctx, c.Catalog, e.ID.String(), e.Region)
	if err != nil {
		return err
	}
	if !complete {
		_, err = c.DB.Exec(ctx, `UPDATE app.osm_region_updates SET state='deleting',slot_held=false,execution_hash=NULL,execution_token=NULL,job_name=NULL,job_uid=NULL,
            executor_outcome=NULL,enqueued_at=clock_timestamp()+interval '30 seconds',error_summary='Region cleanup is retrying.' WHERE id=$1`, e.ID)
		return err
	}
	regions, err := osm.CatalogRegions(ctx, c.Catalog, strings.SplitN(e.Region, ":", 2)[0])
	if err != nil {
		return err
	}
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, region := range regions {
		if region.ID != e.Region {
			continue
		}
		raw, _ := json.Marshal(region)
		if _, err = tx.Exec(ctx, `INSERT INTO app.osm_region_catalog(id,provider,payload) VALUES($1,$2,$3::jsonb) ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload`, region.ID, region.Provider, raw); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE app.osm_region_update_steps SET state='completed',finished_at=COALESCE(finished_at,clock_timestamp()),
        elapsed_total_ms=CASE WHEN state='completed' THEN elapsed_total_ms ELSE NULL END WHERE update_id=$1`, e.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE app.osm_region_updates SET state='succeeded',slot_held=false,execution_token=NULL,execution_hash=NULL,
        finished_at=clock_timestamp(),resumable=false,promoted=false,error_summary=NULL WHERE id=$1`, e.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.osm_region_update_events(update_id,event_type,outcome) VALUES($1,'osm.region_deletion_completed','storage-reclaimed')`, e.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE app.osm_region_updates SET resumable=false WHERE region_id=$1 AND id<>$2 AND NOT slot_held`, e.Region, e.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT app.osm_finalize_durations($1)`, e.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
