package osmcontroller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
)

func (c *Controller) reconcileCheckpointProgress(ctx context.Context, e execution, stopped bool) error {
	if e.Generation == nil {
		return nil
	}
	var needed bool
	if err := c.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.osm_region_update_steps WHERE update_id=$1 AND(state IN('paused','failed') OR($2 AND state='running')))`, e.ID, stopped).Scan(&needed); err != nil {
		return err
	}
	if !needed {
		return nil
	}
	rows, err := c.Catalog.Query(ctx, `SELECT s.stage,s.state,s.cursor,COALESCE(s.finished_at,s.checkpointed_at) FROM osm_catalog.generation_stages s JOIN osm_catalog.generations g ON g.id=s.generation_id WHERE g.id=$1 AND g.control_job_id=$2 AND g.region_id=$3`, *e.Generation, e.ID, e.Region)
	if err != nil {
		return err
	}
	type checkpoint struct {
		orders   []int
		finished *time.Time
	}
	var checkpoints []checkpoint
	for rows.Next() {
		var stage, state string
		var cursor json.RawMessage
		var finished *time.Time
		if err = rows.Scan(&stage, &state, &cursor, &finished); err != nil {
			rows.Close()
			return err
		}
		orders := osm.CompletedCheckpointTasks(stage, state, cursor)
		if len(orders) > 0 && finished != nil {
			checkpoints = append(checkpoints, checkpoint{orders, finished})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, checkpoint := range checkpoints {
		for _, currentOrder := range checkpoint.orders {
			var order int
			if err = tx.QueryRow(ctx, `SELECT task_order FROM app.osm_region_update_steps WHERE update_id=$1 AND name=$2`, e.ID, osm.UpdateTasks[currentOrder-1].Name).Scan(&order); err == pgx.ErrNoRows {
				continue
			} else if err != nil {
				return err
			}
			var amount *int64
			var kind string
			var task *int
			var started time.Time
			err = tx.QueryRow(ctx, `SELECT event_type,task_order,happened_at FROM app.osm_region_update_events
            WHERE update_id=$1 AND happened_at<$2 ORDER BY id DESC LIMIT 1`, e.ID, checkpoint.finished).Scan(&kind, &task, &started)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
			// Only the immediately preceding batch start can bound a lost report.
			// A completion, stop, resume or different task makes that interval unknown.
			if err == nil && kind == "start" && task != nil && *task == order {
				milliseconds := checkpoint.finished.Sub(started).Milliseconds()
				amount = &milliseconds
			}
			changed, err := tx.Query(ctx, `UPDATE app.osm_region_update_steps s SET state='completed',finished_at=GREATEST(s.started_at,$3::timestamptz),elapsed_total_ms=s.elapsed_total_ms+$6::bigint
    FROM app.osm_region_updates u WHERE s.update_id=$1 AND s.task_order=ANY($2::integer[]) AND u.id=s.update_id AND u.generation_id=$4
    AND(s.state IN('paused','failed') OR($5 AND s.state='running')) RETURNING s.task_order`, e.ID, []int{order}, checkpoint.finished, *e.Generation, stopped, amount)
			if err != nil {
				return err
			}
			var orders []int
			for changed.Next() {
				var order int
				if err = changed.Scan(&order); err != nil {
					changed.Close()
					return err
				}
				orders = append(orders, order)
			}
			err = changed.Err()
			changed.Close()
			if err != nil {
				return err
			}
			if len(orders) > 0 {
				if _, err = tx.Exec(ctx, `UPDATE app.osm_region_updates SET elapsed_total_ms=elapsed_total_ms+$2::bigint WHERE id=$1;`, e.ID, amount); err != nil {
					return err
				}
				for _, order := range orders {
					if _, err = tx.Exec(ctx, `INSERT INTO app.osm_region_update_events(update_id,event_type,task_order,outcome) VALUES($1,'executor.checkpoint_reconciled',$2,'completed')`, e.ID, order); err != nil {
						return err
					}
				}
			}
		}
	}
	return tx.Commit(ctx)
}
