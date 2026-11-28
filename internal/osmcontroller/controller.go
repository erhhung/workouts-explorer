package osmcontroller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Controller struct {
	DB, Catalog *pgxpool.Pool
	Jobs        Jobs
	Template    map[string]any
	Concurrency int
	Logger      *slog.Logger
}
type execution struct {
	ID                                                                                      uuid.UUID
	Region, State, Intent, Phase, Image, Token, Name, UID, CleanupName, CleanupUID, Outcome string
	Generation                                                                              *int64
	Reported                                                                                *time.Time
	Promoted                                                                                bool
	Task                                                                                    string
	Operation                                                                               string
}

var pinned = regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}$`)

func (c *Controller) Run(ctx context.Context) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Concurrency < 1 || c.Concurrency > 8 {
		return errors.New("invalid updater concurrency")
	}
	connection, err := c.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	for ctx.Err() == nil {
		var owned bool
		if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('workouts-osm-controller',0))`).Scan(&owned); err != nil {
			return err
		}
		if owned {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
	defer func() {
		unlock, end := context.WithTimeout(context.Background(), 5*time.Second)
		defer end()
		if _, err := connection.Exec(unlock, `SELECT pg_advisory_unlock(hashtextextended('workouts-osm-controller',0))`); err != nil {
			_ = connection.Conn().Close(unlock)
		}
	}()
	for ctx.Err() == nil {
		if err = c.RunOnce(ctx); err != nil {
			c.Logger.WarnContext(ctx, "OSM update reconciliation deferred", "code", "osm-controller-unavailable")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(3 * time.Second):
		}
	}
	return nil
}

func (c *Controller) Validate() (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("invalid updater template")
		}
	}()
	image, ok := c.container()["image"].(string)
	if !ok {
		return errors.New("updater image unavailable")
	}
	_, err = c.specification(execution{ID: uuid.New(), Region: "geofabrik:validation", Image: image, Token: "template-validation"}, "osm-template-validation", "run")
	return err
}

func (c *Controller) RunOnce(ctx context.Context) error {
	rows, err := c.DB.Query(ctx, `SELECT id,region_id,state,intent,phase,COALESCE(image,''),COALESCE(execution_token,''),COALESCE(job_name,''),COALESCE(job_uid,''),COALESCE(cleanup_name,''),COALESCE(cleanup_uid,''),COALESCE(executor_outcome,''),generation_id,reported_at,promoted,
    COALESCE((SELECT name FROM app.osm_region_update_steps WHERE update_id=app.osm_region_updates.id AND task_order=current_order),''),operation FROM app.osm_region_updates WHERE slot_held ORDER BY enqueued_at,id`)
	if err != nil {
		return err
	}
	active := []execution{}
	for rows.Next() {
		var e execution
		if err = rows.Scan(&e.ID, &e.Region, &e.State, &e.Intent, &e.Phase, &e.Image, &e.Token, &e.Name, &e.UID, &e.CleanupName, &e.CleanupUID, &e.Outcome, &e.Generation, &e.Reported, &e.Promoted, &e.Task, &e.Operation); err != nil {
			rows.Close()
			return err
		}
		active = append(active, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range active {
		if err = c.syncBuildSize(ctx, e); err != nil {
			return err
		}
		if err = c.reconcileCheckpointProgress(ctx, e, false); err != nil {
			return err
		}
		if err = c.reconcile(ctx, e); err != nil {
			return err
		}
	}
	for count := 0; count < c.Concurrency; count++ {
		claimed, err := c.claim(ctx)
		if err != nil {
			return err
		}
		if !claimed {
			break
		}
	}
	return nil
}

func (c *Controller) syncBuildSize(ctx context.Context, e execution) error {
	if e.Generation == nil {
		return nil
	}
	var size *int64
	var measured *time.Time
	var id *int64
	err := c.Catalog.QueryRow(ctx, `SELECT CASE WHEN promoted_at IS NULL AND to_regnamespace(schema_name::text) IS NOT NULL THEN build_database_bytes END,
        CASE WHEN promoted_at IS NULL AND to_regnamespace(schema_name::text) IS NOT NULL THEN build_database_measured_at END,
        CASE WHEN promoted_at IS NULL AND to_regnamespace(schema_name::text) IS NOT NULL THEN id END
        FROM osm_catalog.generations WHERE id=$1 AND control_job_id=$2`, *e.Generation, e.ID).Scan(&size, &measured, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"buildDatabaseBytes": size, "buildDatabaseMeasuredAt": measured, "buildDatabaseId": id})
	if err != nil {
		return err
	}
	_, err = c.DB.Exec(ctx, `UPDATE app.osm_region_catalog SET payload=payload||$2::jsonb
        WHERE id=$1 AND (payload->'buildDatabaseBytes' IS DISTINCT FROM ($2::jsonb)->'buildDatabaseBytes'
            OR payload->'buildDatabaseMeasuredAt' IS DISTINCT FROM ($2::jsonb)->'buildDatabaseMeasuredAt'
            OR payload->'buildDatabaseId' IS DISTINCT FROM ($2::jsonb)->'buildDatabaseId')`, e.Region, raw)
	return err
}

func (c *Controller) claim(ctx context.Context) (bool, error) {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var occupied int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM app.osm_region_updates WHERE slot_held`).Scan(&occupied); err != nil {
		return false, err
	}
	if occupied >= c.Concurrency {
		return false, nil
	}
	var id uuid.UUID
	var image string
	err = tx.QueryRow(ctx, `SELECT id,COALESCE(image,'') FROM app.osm_region_updates WHERE NOT slot_held AND state IN('waiting','canceling','deleting') AND enqueued_at<=clock_timestamp() ORDER BY enqueued_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &image)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return false, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	var requeued, restart bool
	if err = tx.QueryRow(ctx, `SELECT state='waiting',state='waiting' AND NOT resumable AND NOT promoted FROM app.osm_region_updates WHERE id=$1`, id).Scan(&requeued, &restart); err != nil {
		return false, err
	}
	if image == "" || requeued {
		image = c.container()["image"].(string)
	}
	if restart {
		if _, err = tx.Exec(ctx, `UPDATE app.osm_region_updates SET generation_id=NULL,current_order=0,phase='Downloading',started_at=NULL WHERE id=$1`, id); err != nil {
			return false, err
		}
	}
	if !pinned.MatchString(image) {
		return false, errors.New("unpinned updater image")
	}
	name := "osm-region-" + strings.ReplaceAll(id.String(), "-", "") + "-" + uuid.New().String()[:8]
	_, err = tx.Exec(ctx, `UPDATE app.osm_region_updates SET slot_held=true,image=$2,execution_token=$3,execution_hash=$4,job_name=$5,
        job_uid=NULL,cleanup_name=NULL,cleanup_uid=NULL,executor_outcome=NULL,reported_at=clock_timestamp() WHERE id=$1`, id, image, token, hex.EncodeToString(digest[:]), name)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (c *Controller) container() map[string]any {
	return c.Template["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
}

func (c *Controller) specification(e execution, name, mode string) (map[string]any, error) {
	raw, _ := json.Marshal(c.Template)
	var job map[string]any
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	if !pinned.MatchString(e.Image) {
		return nil, errors.New("unpinned execution image")
	}
	repository := func(image string) string {
		image = strings.SplitN(image, "@", 2)[0]
		if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
			image = image[:colon]
		}
		return image
	}
	if repository(e.Image) != repository(c.container()["image"].(string)) {
		return nil, errors.New("updater image repository is not approved")
	}
	labels := map[string]any{"app.kubernetes.io/component": "osm-update", "workouts-explorer/region-update": strings.ReplaceAll(e.ID.String(), "-", "")}
	job["apiVersion"] = "batch/v1"
	job["kind"] = "Job"
	job["metadata"] = map[string]any{"name": name, "labels": labels}
	spec := job["spec"].(map[string]any)
	spec["backoffLimit"] = 0
	if _, exists := spec["ttlSecondsAfterFinished"]; !exists {
		spec["ttlSecondsAfterFinished"] = float64(21600)
	}
	ttl, ok := spec["ttlSecondsAfterFinished"].(float64)
	if !ok || ttl < 0 || ttl > math.MaxInt32 || ttl != math.Trunc(ttl) {
		return nil, errors.New("invalid completed updater Job retention")
	}
	pod := spec["template"].(map[string]any)
	pod["metadata"] = map[string]any{"labels": labels}
	ps := pod["spec"].(map[string]any)
	if ps["automountServiceAccountToken"] != false || ps["restartPolicy"] != "Never" {
		return nil, errors.New("unsafe updater pod")
	}
	if ps["hostNetwork"] == true || ps["hostPID"] == true || ps["hostIPC"] == true || ps["initContainers"] != nil || ps["ephemeralContainers"] != nil {
		return nil, errors.New("unsafe updater host access")
	}
	containers, ok := ps["containers"].([]any)
	if !ok || len(containers) != 1 {
		return nil, errors.New("unsafe updater containers")
	}
	container := containers[0].(map[string]any)
	container["image"] = e.Image
	security := container["securityContext"].(map[string]any)
	if security["readOnlyRootFilesystem"] != true || security["allowPrivilegeEscalation"] != false || security["privileged"] == true {
		return nil, errors.New("unsafe updater security context")
	}
	for _, volume := range ps["volumes"].([]any) {
		v := volume.(map[string]any)
		if !((v["name"] == "scratch" && v["emptyDir"] != nil) || (v["name"] == "tls" && v["secret"] != nil)) || v["hostPath"] != nil || v["persistentVolumeClaim"] != nil || v["projected"] != nil {
			return nil, errors.New("unsafe updater volume")
		}
	}
	environment, _ := container["env"].([]any)
	for _, value := range environment {
		name := value.(map[string]any)["name"].(string)
		if name == "SOURCE_KEYRING_FILE" || name == "API_DATABASE_URL" || name == "WORKER_DATABASE_URL" {
			return nil, errors.New("private credential in updater template")
		}
	}
	values := map[string]string{"OSM_CONTROL_JOB_ID": e.ID.String(), "OSM_CONTROL_TOKEN": e.Token, "OSM_REGION_ID": e.Region, "OSM_CONTROL_MODE": mode}
	for _, key := range []string{"OSM_CONTROL_JOB_ID", "OSM_CONTROL_TOKEN", "OSM_REGION_ID", "OSM_CONTROL_MODE"} {
		environment = append(environment, map[string]any{"name": key, "value": values[key]})
	}
	container["env"] = environment
	container["command"] = []any{"/app/osm-update"}
	delete(container, "args")
	return job, nil
}

func executionMatches(job *Job, expected map[string]any) bool {
	var actual map[string]any
	if json.Unmarshal(job.Spec, &actual) != nil {
		return false
	}
	desired := expected["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	template, ok := actual["template"].(map[string]any)
	if !ok {
		return false
	}
	pod, ok := template["spec"].(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"automountServiceAccountToken", "securityContext", "volumes"} {
		if !reflect.DeepEqual(pod[key], desired[key]) {
			return false
		}
	}
	containers, ok := pod["containers"].([]any)
	if !ok || len(containers) != 1 {
		return false
	}
	a := containers[0].(map[string]any)
	b := desired["containers"].([]any)[0].(map[string]any)
	for _, key := range []string{"image", "command", "securityContext", "env", "volumeMounts"} {
		if !reflect.DeepEqual(a[key], b[key]) {
			return false
		}
	}
	return true
}

func (c *Controller) reconcile(ctx context.Context, e execution) error {
	name, uid, mode := e.Name, e.UID, "run"
	if e.Operation == "delete" {
		mode = "delete"
	} else if e.CleanupName != "" {
		name, uid, mode = e.CleanupName, e.CleanupUID, "cleanup"
	} else if e.Intent == "cancel" && e.State == "canceling" && e.Outcome == "" && e.UID == "" {
		mode = "cleanup"
	}
	job, err := c.Jobs.Get(ctx, name)
	if err != nil {
		return err
	}
	if job == nil && uid == "" && e.Outcome == "" {
		spec, err := c.specification(e, name, mode)
		if err != nil {
			return err
		}
		job, err = c.Jobs.Create(ctx, spec)
		if err != nil {
			return err
		}
		if job == nil {
			return errors.New("ambiguous updater creation")
		}
	}
	if job != nil {
		if job.Metadata.Labels["workouts-explorer/region-update"] != strings.ReplaceAll(e.ID.String(), "-", "") || uid != "" && uid != job.Metadata.UID {
			return errors.New("updater execution ownership mismatch")
		}
		if uid == "" {
			expected, err := c.specification(e, name, mode)
			if err != nil {
				return err
			}
			if !executionMatches(job, expected) {
				return errors.New("updater execution template mismatch")
			}
		}
		if uid == "" {
			column := "job_uid"
			if mode == "cleanup" && e.CleanupName != "" {
				column = "cleanup_uid"
			}
			if _, err = c.DB.Exec(ctx, "UPDATE app.osm_region_updates SET "+column+"=$2 WHERE id=$1", e.ID, job.Metadata.UID); err != nil {
				return err
			}
			uid = job.Metadata.UID
		}
		terminal := job.Status.Active == 0 && (job.Status.Succeeded > 0 || job.Status.Failed > 0)
		stale := e.Reported != nil && time.Since(*e.Reported) > 5*time.Minute
		if !terminal {
			if stale && (e.Intent != "run") {
				return c.Jobs.Delete(ctx, name, uid)
			}
			return nil
		}
		if job.Status.Failed > 0 && e.Intent == "run" && e.Outcome != "succeeded" {
			failure, failureErr := c.Jobs.Failure(ctx, strings.ReplaceAll(e.ID.String(), "-", ""), uid)
			if failureErr != nil {
				return failureErr
			}
			summary := failure.Summary()
			if failure.Reason != "OOMKilled" && e.Generation != nil {
				var detail string
				if readErr := c.Catalog.QueryRow(ctx, `SELECT COALESCE(failure_summary,'') FROM osm_catalog.generations WHERE id=$1 AND control_job_id=$2`, *e.Generation, e.ID).Scan(&detail); readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
					return readErr
				}
				if detail != "" {
					summary = failureDetailSummary(detail)
				}
			}
			if summary != "" {
				// Capture termination evidence while the UID-owned pod still exists.
				if _, err = c.DB.Exec(ctx, `UPDATE app.osm_region_updates SET error_summary=$2 WHERE id=$1`, e.ID, summary); err != nil {
					return err
				}
				c.Logger.WarnContext(ctx, "OSM updater execution failed", "update_id", e.ID, "region", e.Region, "task", e.Task, "reason", failure.Reason, "exit_code", failure.ExitCode, "memory_limit", failure.MemoryLimit)
			}
		}
		var retention struct {
			TTL *int `json:"ttlSecondsAfterFinished"`
		}
		if err := json.Unmarshal(job.Spec, &retention); err != nil {
			return err
		}
		if retention.TTL == nil {
			// Pre-retention Jobs have no TTL controller cleanup; keep their old lifecycle.
			return c.Jobs.Delete(ctx, name, uid)
		}
	}
	stopped, err := c.Jobs.PodsStopped(ctx, strings.ReplaceAll(e.ID.String(), "-", ""))
	if err != nil || !stopped {
		return err
	}
	if e.Operation == "delete" {
		return c.finishDeletion(ctx, e)
	}
	if e.Intent == "cancel" && e.Outcome != "canceled" && e.Outcome != "succeeded" {
		if e.CleanupName != "" {
			_, err = c.DB.Exec(ctx, `UPDATE app.osm_region_updates SET cleanup_name=NULL,cleanup_uid=NULL,reported_at=clock_timestamp(),error_summary='Candidate cleanup is retrying.' WHERE id=$1`, e.ID)
			return err
		}
		cleanupName := "osm-cleanup-" + strings.ReplaceAll(e.ID.String(), "-", "") + "-" + uuid.NewString()[:8]
		_, err = c.DB.Exec(ctx, `UPDATE app.osm_region_updates SET cleanup_name=$2,cleanup_uid=NULL,executor_outcome=NULL,reported_at=clock_timestamp() WHERE id=$1`, e.ID, cleanupName)
		return err
	}
	outcome := e.Outcome
	if e.Intent == "pause" && outcome != "succeeded" {
		outcome = "paused"
	}
	if outcome == "" {
		outcome = "failed"
		if e.Intent == "pause" {
			outcome = "paused"
		}
	}
	resumable, promoted := false, e.Promoted
	if e.Generation != nil {
		err = c.Catalog.QueryRow(ctx, `SELECT state='active',state IN('building','validating','evaluating') AND to_regnamespace(schema_name::text) IS NOT NULL
            AND EXISTS(SELECT 1 FROM osm_catalog.generation_stages WHERE generation_id=g.id AND stage='osm2pgsql' AND state='completed') FROM osm_catalog.generations g WHERE id=$1 AND control_job_id=$2`, *e.Generation, e.ID).Scan(&promoted, &resumable)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	regions, err := osm.CatalogRegions(ctx, c.Catalog, strings.SplitN(e.Region, ":", 2)[0])
	if err != nil {
		return err
	}
	for _, region := range regions {
		if region.ID == e.Region {
			raw, _ := json.Marshal(region)
			if _, err = c.DB.Exec(ctx, `INSERT INTO app.osm_region_catalog(id,provider,payload) VALUES($1,$2,$3::jsonb) ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload`, region.ID, region.Provider, raw); err != nil {
				return err
			}
			break
		}
	}
	if outcome == "paused" || outcome == "failed" {
		if err = c.reconcileCheckpointProgress(ctx, e, true); err != nil {
			return err
		}
	}
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE app.osm_region_updates SET state=$2,slot_held=false,execution_token=NULL,execution_hash=NULL,
        finished_at=clock_timestamp(),resumable=$3,promoted=$4,error_summary=CASE $2 WHEN 'paused' THEN 'This update is paused.' WHEN 'canceled' THEN 'This update was canceled.' WHEN 'failed' THEN COALESCE(error_summary,'The updater stopped without a final outcome. Review the updater pod logs for this job.') ELSE NULL END WHERE id=$1`, e.ID, outcome, resumable, promoted)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE app.osm_region_update_steps SET state=$2,finished_at=CASE WHEN $2='paused' THEN NULL ELSE clock_timestamp() END WHERE update_id=$1 AND state='running'`, e.ID, map[string]string{"paused": "paused", "canceled": "canceled", "failed": "failed", "succeeded": "completed"}[outcome])
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT app.osm_finalize_durations($1)`, e.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func failureDetailSummary(detail string) string {
	if index := strings.LastIndex(detail, "ERROR:"); index >= 0 {
		detail = detail[index:]
	}
	text := []rune(detail)
	if len(text) > 256 {
		text = text[:256]
	}
	return string(text)
}
