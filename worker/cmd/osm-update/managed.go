package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type managedState struct {
	RegionID               string `json:"regionId"`
	Intent                 string `json:"intent"`
	GenerationID           *int64 `json:"generationId"`
	Resumable              bool   `json:"resumable"`
	Promoted               bool   `json:"promoted"`
	Operation              string `json:"operation"`
	TaskCount              int    `json:"taskCount"`
	MonitorDatabaseStorage *bool  `json:"monitorDatabaseStorage"`
}
type managedControl struct {
	db                     *pgxpool.Pool
	id                     uuid.UUID
	token                  string
	intent                 atomic.Value
	elapsedSequence        atomic.Int64
	legacyTasks            bool
	monitorDatabaseStorage *bool
}

var errControlUnavailable = errors.New("updater control channel unavailable")

func (c *managedControl) event(ctx context.Context, event osm.UpdateProgress) (managedState, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return managedState{}, err
	}
	var response []byte
	if err = c.db.QueryRow(ctx, `SELECT app.osm_update_event($1,$2,$3::jsonb)`, c.id, c.token, raw).Scan(&response); err != nil {
		return managedState{}, errControlUnavailable
	}
	var state managedState
	if err = json.Unmarshal(response, &state); err != nil {
		return state, err
	}
	c.intent.Store(state.Intent)
	return state, nil
}
func (c *managedControl) Report(ctx context.Context, event osm.UpdateProgress) error {
	// A request's stored inventory is immutable. New executors can resume an
	// older request without renumbering its events or disturbing a running pod.
	if c.legacyTasks {
		if event.Task == 1 {
			if event.Type == "complete" {
				event.Completed = false
			}
			if event.Type == "elapsed" {
				event.Task = 0
			}
		} else if event.Task > 1 {
			event.Task--
		}
	}
	if event.ElapsedMilliseconds != nil && event.ElapsedSequence == 0 {
		event.ElapsedSequence = c.elapsedSequence.Add(1)
	}
	_, err := c.event(ctx, event)
	return err
}

func runManaged(ctx context.Context, getenv func(string) string, stdout io.Writer, writer *pgxpool.Pool) error {
	id, err := uuid.Parse(getenv("OSM_CONTROL_JOB_ID"))
	if err != nil {
		return errors.New("invalid controlled updater identity")
	}
	token := getenv("OSM_CONTROL_TOKEN")
	if len(token) < 32 {
		return errors.New("invalid controlled updater capability")
	}
	app, err := database.OpenWithMaxConns(ctx, getenv("OSM_EXECUTOR_DATABASE_URL"), "workouts-osm-executor", 2)
	if err != nil {
		return err
	}
	defer app.Close()
	control := &managedControl{db: app, id: id, token: token}
	control.intent.Store("run")
	metricsConfigured := getenv("OSM_STORAGE_METRICS_ENABLED") == "true" && strings.TrimSpace(getenv("OSM_PROMETHEUS_URL")) != ""
	state, err := control.event(ctx, osm.UpdateProgress{Type: "read", StorageMetricsConfigured: &metricsConfigured})
	if err != nil {
		return err
	}
	if state.RegionID != getenv("OSM_REGION_ID") {
		return errors.New("controlled updater region mismatch")
	}
	control.legacyTasks = state.Operation != "delete" && state.TaskCount == len(osm.UpdateTasks)-1
	control.monitorDatabaseStorage = state.MonitorDatabaseStorage
	store := osm.PostgreSQLUpdateStore{Pool: writer, ControlJobID: id.String(), WaitForLock: true}
	mode := getenv("OSM_CONTROL_MODE")
	if state.Operation == "delete" {
		if mode != "delete" {
			return errors.New("invalid region deletion execution mode")
		}
		return deleteManaged(ctx, control, store, state)
	}
	if mode == "cleanup" || state.Intent == "cancel" {
		return cancelManaged(ctx, control, store, state)
	}
	if state.Intent == "pause" {
		return control.Report(ctx, osm.UpdateProgress{Type: "finish", Outcome: "paused", Resumable: state.Resumable})
	}
	if mode != "run" {
		return errors.New("invalid controlled updater mode")
	}
	if err = control.Report(ctx, osm.UpdateProgress{Type: "execution"}); err != nil {
		return err
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	monitor, stopMonitor := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	defer func() { stopMonitor(); <-done }()
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-monitor.Done():
				return
			case <-ticker.C:
				heartbeat, end := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, err := control.event(heartbeat, osm.UpdateProgress{Type: "heartbeat"})
				end()
				if err != nil || control.intent.Load() != "run" {
					cancel()
				}
			}
		}
	}()
	var updateErr error
	if state.Promoted {
		updateErr = finishPromotedMaintenance(work, control, store)
	} else {
		resume := int64(0)
		if state.Resumable && state.GenerationID != nil {
			resume = *state.GenerationID
		}
		if !state.Resumable {
			if updateErr = discardUncheckpointedCandidate(work, store, id, state.RegionID); updateErr != nil {
				return updateErr
			}
		}
		updateErr = runControlledUpdate(work, getenv, stdout, writer, store, resume, control)
	}
	cancel()
	finish, end := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer end()
	intent, _ := control.intent.Load().(string)
	if updateErr != nil && intent == "cancel" {
		return cancelManaged(finish, control, store, state)
	}
	outcome := "succeeded"
	if updateErr != nil {
		outcome = "failed"
		if intent == "pause" {
			outcome = "paused"
		}
	}
	var generation int64
	var resumable, promoted bool
	err = writer.QueryRow(finish, `SELECT g.id,g.state='active',g.state IN('building','validating','evaluating')
        AND EXISTS(SELECT 1 FROM osm_catalog.generation_stages s WHERE s.generation_id=g.id AND s.stage='osm2pgsql' AND s.state='completed')
        AND to_regnamespace(g.schema_name::text) IS NOT NULL FROM osm_catalog.generations g WHERE control_job_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&generation, &promoted, &resumable)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return errors.New("could not verify updater outcome")
	}
	if err = control.Report(finish, osm.UpdateProgress{Type: "finish", Outcome: outcome, Resumable: resumable, Promoted: promoted, ElapsedIncomplete: errors.Is(updateErr, errControlUnavailable)}); err != nil {
		return err
	}
	if updateErr != nil {
		_, _ = fmt.Fprintln(stdout, "Controlled updater stopped; durable progress retained.")
	}
	return updateErr
}

func finishPromotedMaintenance(ctx context.Context, control osm.UpdateReporter, store interface {
	ProcessStorageGC(context.Context) error
	RefreshStatistics(context.Context) error
}) error {
	// Reopen the final task when retrying maintenance left by an older executor too.
	if err := control.Report(ctx, osm.UpdateProgress{Type: "start", Task: len(osm.UpdateTasks)}); err != nil {
		return err
	}
	for _, maintain := range []func(context.Context) error{store.ProcessStorageGC, store.RefreshStatistics} {
		started := time.Now()
		if err := maintain(ctx); err != nil {
			return err
		}
		amount := time.Since(started).Milliseconds()
		if err := control.Report(ctx, osm.UpdateProgress{Type: "elapsed", Task: len(osm.UpdateTasks), ElapsedMilliseconds: &amount}); err != nil {
			return err
		}
	}
	return control.Report(ctx, osm.UpdateProgress{Type: "settled", Task: len(osm.UpdateTasks), Completed: true})
}

func deleteManaged(ctx context.Context, control *managedControl, store osm.PostgreSQLUpdateStore, state managedState) error {
	live, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	defer func() { stop(); <-done }()
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-live.Done():
				return
			case <-ticker.C:
				heartbeat, end := context.WithTimeout(live, 5*time.Second)
				_, err := control.event(heartbeat, osm.UpdateProgress{Type: "heartbeat"})
				end()
				if err != nil {
					stop()
					return
				}
			}
		}
	}()
	err := store.DeleteRegion(live, control.id.String(), state.RegionID, control)
	finish, end := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer end()
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	if reportErr := control.Report(finish, osm.UpdateProgress{Type: "finish", Outcome: outcome}); reportErr != nil {
		return reportErr
	}
	return err
}

func discardUncheckpointedCandidate(ctx context.Context, store osm.PostgreSQLUpdateStore, request uuid.UUID, region string) error {
	var generation int64
	err := store.Pool.QueryRow(ctx, `SELECT id FROM osm_catalog.generations g WHERE control_job_id=$1 AND region_id=$2 AND state IN('building','validating','evaluating')
    AND NOT EXISTS(SELECT 1 FROM osm_catalog.generation_stages s WHERE s.generation_id=g.id AND s.stage='osm2pgsql' AND s.state='completed') ORDER BY id DESC LIMIT 1`, request, region).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	unlock, err := store.LockUpdater(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return store.Abort(ctx, generation, region, "Restarting source/import prerequisites after an uncheckpointed execution")
}

func cancelManaged(ctx context.Context, control *managedControl, store osm.PostgreSQLUpdateStore, state managedState) error {
	cleanup, end := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer end()
	var resources bool
	if err := store.Pool.QueryRow(cleanup, `SELECT EXISTS(SELECT 1 FROM osm_catalog.generations WHERE control_job_id=$1 AND region_id=$2
        AND(state='active' OR to_regnamespace(schema_name::text) IS NOT NULL))`, control.id, state.RegionID).Scan(&resources); err != nil {
		return err
	}
	if !resources {
		return control.Report(cleanup, osm.UpdateProgress{Type: "finish", Outcome: "canceled"})
	}
	live, stop := context.WithCancel(cleanup)
	defer stop()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-live.Done():
				return
			case <-ticker.C:
				heartbeat, done := context.WithTimeout(live, 5*time.Second)
				_ = control.Report(heartbeat, osm.UpdateProgress{Type: "heartbeat"})
				done()
			}
		}
	}()
	unlock, err := store.LockUpdater(cleanup)
	if err != nil {
		return err
	}
	defer unlock()
	var id int64
	var schema, status string
	err = store.Pool.QueryRow(cleanup, `SELECT id,schema_name::text,state FROM osm_catalog.generations WHERE control_job_id=$1 AND region_id=$2 ORDER BY id DESC LIMIT 1`, control.id, state.RegionID).Scan(&id, &schema, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return control.Report(cleanup, osm.UpdateProgress{Type: "finish", Outcome: "canceled"})
	}
	if err != nil {
		return err
	}
	if status == "active" {
		err = finishPromotedMaintenance(cleanup, control, store)
		if err != nil {
			return err
		}
		return control.Report(cleanup, osm.UpdateProgress{Type: "finish", Outcome: "succeeded", Promoted: true})
	}
	if schema != fmt.Sprintf("osm_build_%d", id) || !strings.HasPrefix(schema, "osm_build_") {
		return errors.New("candidate identity mismatch")
	}
	if status == "building" || status == "validating" || status == "evaluating" {
		err = store.Abort(cleanup, id, state.RegionID, "Canceled by administrator")
	} else if status == "failed" {
		_, err = store.Pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	} else {
		return errors.New("candidate is not cancellable")
	}
	if err != nil {
		return err
	}
	var gone bool
	if err = store.Pool.QueryRow(cleanup, `SELECT to_regnamespace($1) IS NULL`, schema).Scan(&gone); err != nil || !gone {
		return errors.New("candidate cleanup incomplete")
	}
	return control.Report(cleanup, osm.UpdateProgress{Type: "finish", Outcome: "canceled"})
}
