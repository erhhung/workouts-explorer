package osm

import (
	"context"
	"time"
)

type UpdateTaskDefinition struct {
	Order    int    `json:"order"`
	Name     string `json:"name"`
	Repeated bool   `json:"repeated"`
}

// This is the public logical inventory, not the number of SQL invocations.
// Propagation may revisit compress/hook while retaining the same task rows.
var UpdateTasks = func() []UpdateTaskDefinition {
	names := []string{"prepare-source", "download", "tags-filter", "check-refs", "osm2pgsql", "postprocess",
		"derive/init", "derive/node-counts", "derive/shared-nodes", "derive/segments", "derive/path-primary", "derive/source-unique", "derive/geography-index", "derive/start-index", "derive/end-index", "derive/logical-index", "derive/locality-index", "derive/logical-locality-index", "derive/finalize",
		"clip-candidates", "clip-replacements", "clip-apply", "clip-residual", "clip-finalize", "attribute-parks-tags", "attribute-education", "attribute-slivers/discover", "attribute-slivers/apply",
		"identity-segments", "identity-edges/orientation-1", "identity-edges/orientation-2", "identity-edges/orientation-4", "identity-edges/finalize", "identity-proximity/roads", "identity-proximity/candidates", "identity-proximity/edges",
		"identity-components/branch-nodes", "identity-components/branch-classes", "identity-components/filter-edges", "identity-components/components-left", "identity-components/components-right", "identity-components/component-index",
		"identity-propagate/compress", "identity-propagate/hook", "identity-propagate/assign", "identity-label-overrides/init", "identity-label-overrides/labels", "identity-label-overrides/neighbors", "identity-rewrite", "prepare-partitions",
		"validate/init", "validate/ways", "validate/segments", "validate/logical-paths", "validate/locality-residuals", "validate/parks", "validate/education", "validate/finalize"}
	repeated := map[int]bool{8: true, 10: true, 20: true, 21: true, 22: true, 23: true, 25: true, 26: true, 27: true, 28: true, 29: true, 30: true, 31: true, 32: true, 34: true, 35: true, 36: true, 39: true, 40: true, 41: true, 43: true, 44: true, 45: true, 47: true, 49: true, 54: true, 55: true}
	result := make([]UpdateTaskDefinition, len(names))
	for i, name := range names {
		result[i] = UpdateTaskDefinition{i + 1, name, repeated[i+1] || name == "postprocess"}
	}
	return result
}()

var DeleteTasks = []UpdateTaskDefinition{
	{Order: 1, Name: "detach-region"},
	{Order: 2, Name: "reclaim-storage"},
	{Order: 3, Name: "verify-cleanup"},
}

type RegionUpdateStep struct {
	UpdateTaskDefinition
	State        string     `json:"state"`
	Batch        int        `json:"batch"`
	Batches      int        `json:"batches"`
	StartedAt    *time.Time `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	ElapsedTotal *float64   `json:"elapsedTotal"`
}

type RegionUpdate struct {
	ID                     string             `json:"id"`
	RegionID               string             `json:"regionId"`
	State                  string             `json:"state"`
	Phase                  string             `json:"phase"`
	Operation              string             `json:"operation"`
	MonitorDatabaseStorage bool               `json:"monitorDatabaseStorage"`
	StoragePolicyRevision  int64              `json:"storagePolicyRevision"`
	BuildID                *int64             `json:"buildId"`
	Resumable              bool               `json:"resumable"`
	Promoted               bool               `json:"promoted"`
	CurrentOrder           int                `json:"currentOrder"`
	CreatedAt              time.Time          `json:"createdAt"`
	StartedAt              *time.Time         `json:"startedAt"`
	FinishedAt             *time.Time         `json:"finishedAt"`
	ElapsedTotal           *float64           `json:"elapsedTotal"`
	ElapsedSince           *time.Time         `json:"elapsedSince,omitempty"`
	ExecutionElapsed       *float64           `json:"executionElapsed"`
	ExecutionSince         *time.Time         `json:"executionSince"`
	Error                  *string            `json:"error"`
	Cleared                bool               `json:"cleared"`
	Steps                  []RegionUpdateStep `json:"steps,omitempty"`
}

const RegionUpdateJSON = `jsonb_build_object('id',upper(replace(id::text,'-','')),'regionId',region_id,'state',state,'phase',phase,
    'operation',operation,'monitorDatabaseStorage',monitor_database_storage,'storagePolicyRevision',storage_policy_revision,'buildId',generation_id,'resumable',resumable,'promoted',promoted,'currentOrder',current_order,
    'createdAt',created_at,'startedAt',started_at,'finishedAt',finished_at,'error',error_summary,'elapsedTotal',elapsed_total_ms/1000.0,
    'elapsedSince',CASE WHEN state IN('downloading','processing') AND EXISTS(
        SELECT 1 FROM app.osm_region_update_steps running WHERE running.update_id=app.osm_region_updates.id AND running.task_order=current_order AND running.state='running') THEN (
        SELECT min(started.happened_at) FROM app.osm_region_update_events started
        WHERE started.update_id=app.osm_region_updates.id AND started.event_type='start' AND started.task_order=current_order
          AND started.id>COALESCE((SELECT max(boundary.id) FROM app.osm_region_update_events boundary
              WHERE boundary.update_id=app.osm_region_updates.id AND boundary.event_type IN('complete','finish','osm.region_update_resume')),0)) END,
    'cleared',EXISTS(SELECT 1 FROM app.osm_region_update_events cleared WHERE cleared.update_id=app.osm_region_updates.id AND cleared.event_type='osm.region_update_clear')) || (
    WITH boundaries AS (
        SELECT event_type,happened_at,count(*) FILTER(WHERE event_type IN('finish','executor.failed','executor.stopped','osm.region_update_resume'))
            OVER(ORDER BY happened_at,id ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS execution
        FROM app.osm_region_update_events WHERE update_id=app.osm_region_updates.id
    ), executions AS (
        SELECT execution,min(happened_at) FILTER(WHERE event_type IN('start','execution')) AS started,
            max(happened_at) FILTER(WHERE event_type IN('finish','executor.failed','executor.stopped')) AS finished
        FROM boundaries GROUP BY execution
    ) SELECT jsonb_build_object('executionElapsed',COALESCE(sum(GREATEST(extract(epoch FROM finished-started),0)) FILTER(WHERE finished IS NOT NULL AND started IS NOT NULL),0),
        'executionSince',CASE WHEN state IN('waiting','downloading','processing','deleting','pausing','canceling') THEN
            (SELECT started FROM executions WHERE started IS NOT NULL AND finished IS NULL ORDER BY execution DESC LIMIT 1) END)
    FROM executions)`

type UpdateProgress struct {
	StorageMetricsConfigured *bool  `json:"storageMetricsConfigured,omitempty"`
	Type                     string `json:"type"`
	Task                     int    `json:"task,omitempty"`
	GenerationID             int64  `json:"generationId,omitempty"`
	Batch                    int    `json:"batch,omitempty"`
	Batches                  int    `json:"batches,omitempty"`
	Completed                bool   `json:"completed,omitempty"`
	Outcome                  string `json:"outcome,omitempty"`
	Resumable                bool   `json:"resumable,omitempty"`
	Promoted                 bool   `json:"promoted,omitempty"`
	Error                    string `json:"error,omitempty"`
	ElapsedMilliseconds      *int64 `json:"elapsedMilliseconds,omitempty"`
	ElapsedSequence          int64  `json:"elapsedSequence,omitempty"`
	ElapsedIncomplete        bool   `json:"elapsedIncomplete,omitempty"`
	AwaitingMetrics          bool   `json:"awaitingMetrics,omitempty"`
}

type UpdateReporter interface {
	Report(context.Context, UpdateProgress) error
}

func (u Updater) report(ctx context.Context, event UpdateProgress) error {
	if u.Progress == nil {
		return nil
	}
	return u.Progress.Report(ctx, event)
}

func (u Updater) reportElapsed(ctx context.Context, event UpdateProgress, duration time.Duration) error {
	milliseconds := duration.Milliseconds()
	if milliseconds < 0 {
		milliseconds = 0
	}
	event.ElapsedMilliseconds = &milliseconds
	return u.report(ctx, event)
}

func logicalTask(stage, phase string) UpdateTaskDefinition {
	if stage == "residential-context" {
		task := UpdateTasks[5]
		task.Repeated = true
		return task
	}
	if phase == "" {
		phase = map[string]string{"derive": "init", "attribute-slivers": "discover", "identity-edges": "orientation-1", "identity-proximity": "roads", "identity-components": "branch-nodes", "identity-propagate": "compress", "identity-label-overrides": "init", "validate": "init"}[stage]
	}
	name := stage
	if phase != "" {
		name += "/" + phase
	}
	for _, task := range UpdateTasks {
		if task.Name == name {
			return task
		}
	}
	return UpdateTaskDefinition{}
}
