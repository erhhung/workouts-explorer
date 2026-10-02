package osm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgreSQLUpdateStore struct{ Pool *pgxpool.Pool }

type GenerationStatus struct {
	Found             bool             `json:"found"`
	NextGenerationID  int64            `json:"nextGenerationId"`
	DerivationVersion int              `json:"derivationVersion"`
	GenerationID      int64            `json:"generationId,omitempty"`
	RegionID          string           `json:"regionId,omitempty"`
	State             string           `json:"state,omitempty"`
	SchemaName        string           `json:"schemaName,omitempty"`
	Failure           string           `json:"failure,omitempty"`
	Stage             *GenerationStage `json:"currentStage,omitempty"`
	Block             *GenerationBlock `json:"block,omitempty"`
}

type GenerationStage struct {
	Name           string          `json:"name"`
	State          string          `json:"state"`
	BatchCount     int64           `json:"batchCount"`
	RowsProcessed  int64           `json:"rowsProcessed"`
	Cursor         json.RawMessage `json:"cursor"`
	CheckpointedAt *time.Time      `json:"checkpointedAt,omitempty"`
	Failure        string          `json:"failure,omitempty"`
}

type GenerationBlock struct {
	ReasonCode        string     `json:"reasonCode"`
	PostgresPrimary   string     `json:"postgresPrimary,omitempty"`
	PVCName           string     `json:"pvcName,omitempty"`
	CapacityBytes     *int64     `json:"capacityBytes,omitempty"`
	FreeBytes         *int64     `json:"freeBytes,omitempty"`
	RequiredFreeBytes *int64     `json:"requiredFreeBytes,omitempty"`
	BlockedAt         time.Time  `json:"blockedAt"`
	ClearedAt         *time.Time `json:"clearedAt,omitempty"`
	ClearedBy         string     `json:"clearedBy,omitempty"`
}

func (s PostgreSQLUpdateStore) LockUpdater(ctx context.Context) (func(), error) {
	connection, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	release := func() { connection.Release() }
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('workouts-osm-updater',0))`).Scan(&locked); err != nil {
		release()
		return nil, err
	}
	if !locked {
		release()
		return nil, errors.New("another OSM updater owns the database-wide advisory lock")
	}
	unlock := func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('workouts-osm-updater',0))`)
		release()
	}

	rows, err := connection.Query(ctx, `SELECT pid,pg_terminate_backend(pid,5000)
		FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
		AND application_name='workouts-osm-update' ORDER BY pid`)
	if err != nil {
		unlock()
		return nil, err
	}
	for rows.Next() {
		var pid int32
		var terminated bool
		if err := rows.Scan(&pid, &terminated); err != nil {
			rows.Close()
			unlock()
			return nil, err
		}
		if !terminated {
			rows.Close()
			unlock()
			return nil, fmt.Errorf("stale OSM updater backend %d could not be terminated", pid)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		unlock()
		return nil, err
	}
	rows.Close()

	var staleUpdaterLocks, foreignConflictingLocks int
	err = connection.QueryRow(ctx, `SELECT
		count(*) FILTER(WHERE activity.application_name='workouts-osm-update'),
		count(*) FILTER(WHERE activity.application_name<>'workouts-osm-update')
	FROM pg_locks held
	JOIN pg_stat_activity activity ON activity.pid=held.pid
	LEFT JOIN pg_class relation ON relation.oid=held.relation
	LEFT JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
	WHERE held.pid<>pg_backend_pid() AND held.granted
	AND (namespace.nspname LIKE 'osm_build_%' OR
		(namespace.nspname='osm_catalog' AND relation.relname IN ('generation_stages','generation_storage_blocks')))
	AND held.mode NOT IN ('AccessShareLock','RowShareLock')`).Scan(&staleUpdaterLocks, &foreignConflictingLocks)
	if err != nil {
		unlock()
		return nil, err
	}
	if staleUpdaterLocks != 0 || foreignConflictingLocks != 0 {
		unlock()
		return nil, fmt.Errorf("relevant PostgreSQL resources retain %d updater and %d foreign conflicting locks", staleUpdaterLocks, foreignConflictingLocks)
	}
	return unlock, nil
}

func (s PostgreSQLUpdateStore) LockGeneration(ctx context.Context, id int64) (func(), error) {
	connection, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('workouts-osm-generation',$1))`, id).Scan(&locked); err != nil || !locked {
		connection.Release()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("generation %d is already being updated", id)
	}
	return func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('workouts-osm-generation',$1))`, id)
		connection.Release()
	}, nil
}

func (s PostgreSQLUpdateStore) Resume(ctx context.Context, id int64, regionID string, versions ToolVersions) (Generation, map[string]bool, error) {
	var blocked bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM osm_catalog.generation_storage_blocks WHERE generation_id=$1 AND cleared_at IS NULL)`, id).Scan(&blocked); err != nil {
		return Generation{}, nil, err
	}
	if blocked {
		return Generation{}, nil, fmt.Errorf("generation %d is operator-blocked; run unblock before resuming", id)
	}
	var generation Generation
	err := s.Pool.QueryRow(ctx, `SELECT id,region_id,schema_name::text,source_url FROM osm_catalog.generations
		WHERE id=$1 AND region_id=$2 AND state IN ('building','validating') AND importer_version=$3 AND derivation_version=$4
		AND osmium_version=$5 AND osm2pgsql_version=$6 AND source_sha256 IS NOT NULL AND source_header_timestamp IS NOT NULL
		AND EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name=generations.schema_name::text)`,
		id, regionID, ImporterVersion, DerivationVersion, versions.Osmium, versions.Osm2pgsql).Scan(&generation.ID, &generation.RegionID, &generation.SchemaName, &generation.SourceURL)
	if err != nil {
		return Generation{}, nil, fmt.Errorf("generation %d is not resumable: %w", id, err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT stage FROM osm_catalog.generation_stages WHERE generation_id=$1 AND state='completed'`, id)
	if err != nil {
		return Generation{}, nil, err
	}
	defer rows.Close()
	completed := map[string]bool{}
	for rows.Next() {
		var stage string
		if err := rows.Scan(&stage); err != nil {
			return Generation{}, nil, err
		}
		completed[stage] = true
	}
	if err := rows.Err(); err != nil {
		return Generation{}, nil, err
	}
	if !completed["osm2pgsql"] {
		return Generation{}, nil, errors.New("generation has no durable osm2pgsql checkpoint")
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generations SET state='building',validation='{}'::jsonb,
		validated_at=NULL,failure_summary=NULL WHERE id=$1 AND state='validating'`, id); err != nil {
		return Generation{}, nil, err
	}
	foundIncomplete := false
	for _, stage := range pipelineStages {
		if stage == "validate" {
			continue
		}
		if !completed[stage] {
			foundIncomplete = true
			continue
		}
		if foundIncomplete {
			return Generation{}, nil, fmt.Errorf("generation %d has completed stage %s after an incomplete predecessor", id, stage)
		}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE osm_catalog.generations SET failure_summary=NULL WHERE id=$1`, id)
	return generation, completed, err
}

func (s PostgreSQLUpdateStore) RecordStorageBlock(ctx context.Context, generation Generation, stage string, blocked *StoragePreflightError) error {
	observation := blocked.Observation
	measurementsKnown := observation.CapacityBytes > 0 || blocked.Code == StorageInsufficientSpace
	command, err := s.Pool.Exec(ctx, `INSERT INTO osm_catalog.generation_storage_blocks(
		generation_id,stage,batch_count,cursor,reason_code,postgres_primary,namespace,pod_name,pvc_name,
		capacity_bytes,free_bytes,required_free_bytes)
		SELECT $1,$2,coalesce(stage.batch_count,0),coalesce(stage.cursor,'{}'::jsonb),$3,$4,$5,$6,$7,
			$8,$9,$10 FROM osm_catalog.generations generation
		LEFT JOIN osm_catalog.generation_stages stage ON stage.generation_id=generation.id AND stage.stage=$2
		WHERE generation.id=$1 AND generation.region_id=$11 AND generation.state='building'
		ON CONFLICT(generation_id) WHERE cleared_at IS NULL DO UPDATE SET
			stage=excluded.stage,batch_count=excluded.batch_count,cursor=excluded.cursor,reason_code=excluded.reason_code,
			postgres_primary=excluded.postgres_primary,namespace=excluded.namespace,pod_name=excluded.pod_name,
			pvc_name=excluded.pvc_name,capacity_bytes=excluded.capacity_bytes,free_bytes=excluded.free_bytes,
			required_free_bytes=excluded.required_free_bytes,blocked_at=transaction_timestamp()`,
		generation.ID, stage, blocked.Code, nullableText(observation.PostgresPrimary), nullableText(observation.Namespace),
		nullableText(observation.PodName), nullableText(observation.PVCName), nullableMeasurement(observation.CapacityBytes, measurementsKnown),
		nullableMeasurement(observation.FreeBytes, measurementsKnown), nullableMeasurement(observation.RequiredFreeBytes, measurementsKnown), generation.RegionID)
	if err == nil && command.RowsAffected() != 1 {
		err = fmt.Errorf("generation %d is not building", generation.ID)
	}
	return err
}

func (s PostgreSQLUpdateStore) Unblock(ctx context.Context, generationID int64, approvedBy, note string) error {
	approvedBy, note = strings.TrimSpace(approvedBy), strings.TrimSpace(note)
	if approvedBy == "" || len(approvedBy) > 200 || len(note) > 1000 {
		return errors.New("valid storage-block approver and note are required")
	}
	command, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generation_storage_blocks block
		SET cleared_at=transaction_timestamp(),cleared_by=$2,clearance_note=NULLIF($3,'')
		FROM osm_catalog.generations generation WHERE block.generation_id=$1 AND block.cleared_at IS NULL
		AND generation.id=block.generation_id AND generation.state='building'`, generationID, approvedBy, note)
	if err == nil && command.RowsAffected() != 1 {
		err = fmt.Errorf("generation %d has no active storage block", generationID)
	}
	return err
}

func (s PostgreSQLUpdateStore) Status(ctx context.Context, generationID int64, regionID string) (GenerationStatus, error) {
	status := GenerationStatus{DerivationVersion: DerivationVersion}
	if err := s.Pool.QueryRow(ctx, `SELECT last_value+CASE WHEN is_called THEN 1 ELSE 0 END FROM osm_catalog.generations_id_seq`).Scan(&status.NextGenerationID); err != nil {
		return status, err
	}
	query := `SELECT id,region_id,state,schema_name::text,coalesce(failure_summary,'') FROM osm_catalog.generations
		WHERE ($1>0 AND id=$1) OR ($1=0 AND region_id=$2 AND state IN ('building','validating','evaluating'))
		ORDER BY id DESC LIMIT 1`
	if err := s.Pool.QueryRow(ctx, query, generationID, regionID).Scan(&status.GenerationID, &status.RegionID, &status.State, &status.SchemaName, &status.Failure); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return status, nil
		}
		return status, err
	}
	status.Found = true
	var stage GenerationStage
	err := s.Pool.QueryRow(ctx, `SELECT stage,state,batch_count,rows_processed,cursor,checkpointed_at,coalesce(failure_summary,'')
		FROM osm_catalog.generation_stages WHERE generation_id=$1 ORDER BY stage_order DESC LIMIT 1`, status.GenerationID).
		Scan(&stage.Name, &stage.State, &stage.BatchCount, &stage.RowsProcessed, &stage.Cursor, &stage.CheckpointedAt, &stage.Failure)
	if err == nil {
		status.Stage = &stage
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return status, err
	}
	var block GenerationBlock
	err = s.Pool.QueryRow(ctx, `SELECT reason_code,coalesce(postgres_primary,''),coalesce(pvc_name,''),capacity_bytes,
		free_bytes,required_free_bytes,blocked_at,cleared_at,coalesce(cleared_by,'')
		FROM osm_catalog.generation_storage_blocks WHERE generation_id=$1 ORDER BY id DESC LIMIT 1`, status.GenerationID).
		Scan(&block.ReasonCode, &block.PostgresPrimary, &block.PVCName, &block.CapacityBytes, &block.FreeBytes,
			&block.RequiredFreeBytes, &block.BlockedAt, &block.ClearedAt, &block.ClearedBy)
	if err == nil {
		status.Block = &block
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return status, err
	}
	return status, nil
}

func (s PostgreSQLUpdateStore) Abort(ctx context.Context, generationID int64, regionID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 512 {
		return errors.New("abort reason must contain 1-512 characters")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var schema string
	if err := tx.QueryRow(ctx, `SELECT schema_name::text FROM osm_catalog.generations
		WHERE id=$1 AND region_id=$2 AND state IN ('building','validating','evaluating') FOR UPDATE`, generationID, regionID).Scan(&schema); err != nil {
		return fmt.Errorf("generation %d cannot be aborted: %w", generationID, err)
	}
	if schema != "osm_build_"+strconv.FormatInt(generationID, 10) {
		return errors.New("generation build schema does not match its ID")
	}
	if _, err := tx.Exec(ctx, `UPDATE osm_catalog.generations SET state='failed',failure_summary=$2 WHERE id=$1`, generationID, safeFailure(errors.New(reason))); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableMeasurement(value int64, known bool) any {
	if !known {
		return nil
	}
	return value
}

func (s PostgreSQLUpdateStore) StartStage(ctx context.Context, id int64, stage string, order int, fence string) error {
	command, err := s.Pool.Exec(ctx, `INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state,started_at,finished_at,failure_summary)
		VALUES($1,$2,$3,$4,'running',transaction_timestamp(),NULL,NULL) ON CONFLICT(generation_id,stage) DO UPDATE
		SET state='running',fence=EXCLUDED.fence,finished_at=NULL,failure_summary=NULL,
			cursor=CASE WHEN EXCLUDED.stage='validate' AND generation_stages.state='completed' THEN '{}'::jsonb ELSE generation_stages.cursor END,
			rows_processed=CASE WHEN EXCLUDED.stage='validate' AND generation_stages.state='completed' THEN 0 ELSE generation_stages.rows_processed END,
			batch_count=CASE WHEN EXCLUDED.stage='validate' AND generation_stages.state='completed' THEN 0 ELSE generation_stages.batch_count END,
			checkpointed_at=CASE WHEN EXCLUDED.stage='validate' AND generation_stages.state='completed' THEN NULL ELSE generation_stages.checkpointed_at END
		WHERE generation_stages.stage_order=EXCLUDED.stage_order AND (
			generation_stages.fence=EXCLUDED.fence OR (
				generation_stages.state='failed' AND generation_stages.batch_count=0
				AND generation_stages.rows_processed=0 AND generation_stages.cursor='{}'::jsonb
			)
		)`, id, stage, order, fence)
	if err == nil && command.RowsAffected() != 1 {
		err = fmt.Errorf("stage %s fence changed", stage)
	}
	return err
}
func (s PostgreSQLUpdateStore) StageCompleted(ctx context.Context, id int64, stage string) (bool, error) {
	var completed bool
	err := s.Pool.QueryRow(ctx, `SELECT state='completed' FROM osm_catalog.generation_stages WHERE generation_id=$1 AND stage=$2`, id, stage).Scan(&completed)
	return completed, err
}
func (s PostgreSQLUpdateStore) StageCursor(ctx context.Context, id int64, stage string) (json.RawMessage, error) {
	var cursor json.RawMessage
	err := s.Pool.QueryRow(ctx, `SELECT cursor FROM osm_catalog.generation_stages WHERE generation_id=$1 AND stage=$2`, id, stage).Scan(&cursor)
	return cursor, err
}
func (s PostgreSQLUpdateStore) StageFenceMatches(ctx context.Context, id int64, stage, fence string) (bool, error) {
	var matches bool
	err := s.Pool.QueryRow(ctx, `SELECT fence=$3 FROM osm_catalog.generation_stages WHERE generation_id=$1 AND stage=$2`, id, stage, fence).Scan(&matches)
	return matches, err
}
func (s PostgreSQLUpdateStore) CompleteStage(ctx context.Context, id int64, stage string) error {
	command, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generation_stages stage SET state='completed',finished_at=transaction_timestamp(),failure_summary=NULL
		FROM osm_catalog.generations generation WHERE stage.generation_id=$1 AND stage.stage=$2 AND stage.state='running'
		AND generation.id=stage.generation_id AND generation.state IN ('building','evaluating')`, id, stage)
	if err == nil && command.RowsAffected() != 1 {
		err = fmt.Errorf("stage %s is not running", stage)
	}
	return err
}
func (s PostgreSQLUpdateStore) FailStage(ctx context.Context, generation Generation, stage, summary string) error {
	if _, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generation_stages SET state='failed',finished_at=transaction_timestamp(),failure_summary=$3
		WHERE generation_id=$1 AND stage=$2 AND state<>'completed'`, generation.ID, stage, summary); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generations SET failure_summary=$2 WHERE id=$1 AND state='building'`, generation.ID, summary)
	return err
}

func (s PostgreSQLUpdateStore) Reserve(ctx context.Context, regionID string, versions ToolVersions) (Generation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Generation{}, err
	}
	defer tx.Rollback(ctx)
	var generation Generation
	err = tx.QueryRow(ctx, `
		WITH region AS (
			SELECT id,source_url FROM osm_catalog.regions WHERE id=$1 AND configured FOR SHARE
		), allocated AS (
			SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id')) AS id
		)
		INSERT INTO osm_catalog.generations(
			id,region_id,state,schema_name,source_url,osmium_version,osm2pgsql_version,
			importer_version,derivation_version
		) OVERRIDING SYSTEM VALUE
		SELECT allocated.id,region.id,'building',('osm_build_'||allocated.id)::name,
			region.source_url,$2,$3,$4,$5 FROM region CROSS JOIN allocated
		RETURNING id,region_id,schema_name::text,source_url`, regionID, versions.Osmium,
		versions.Osm2pgsql, ImporterVersion, DerivationVersion).Scan(
		&generation.ID, &generation.RegionID, &generation.SchemaName, &generation.SourceURL)
	if err != nil {
		return Generation{}, err
	}
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{generation.SchemaName}.Sanitize()); err != nil {
		return Generation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Generation{}, err
	}
	return generation, nil
}

func (s PostgreSQLUpdateStore) ReserveEvaluation(ctx context.Context, regionID string, versions ToolVersions) (Generation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Generation{}, err
	}
	defer tx.Rollback(ctx)
	var generation Generation
	err = tx.QueryRow(ctx, `WITH region AS (
		SELECT id,source_url FROM osm_catalog.regions WHERE id=$1 AND configured FOR SHARE
	), allocated AS (SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id')) id)
	INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,osmium_version,osm2pgsql_version,importer_version,derivation_version)
	OVERRIDING SYSTEM VALUE SELECT allocated.id,region.id,'evaluating',('osm_build_'||allocated.id)::name,
		region.source_url,$2,$3,$4,$5 FROM region CROSS JOIN allocated
	RETURNING id,region_id,schema_name::text,source_url`, regionID, versions.Osmium, versions.Osm2pgsql,
		ImporterVersion, DerivationVersion).Scan(&generation.ID, &generation.RegionID, &generation.SchemaName, &generation.SourceURL)
	if err != nil {
		return Generation{}, err
	}
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{generation.SchemaName}.Sanitize()); err != nil {
		return Generation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Generation{}, err
	}
	return generation, nil
}

func (s PostgreSQLUpdateStore) RecordDownload(ctx context.Context, id int64, result DownloadResult, timestamp time.Time) error {
	command, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generations SET downloaded_bytes=$2,source_sha256=$3,source_header_timestamp=$4 WHERE id=$1 AND state='building'`, id, result.Bytes, result.SHA256, timestamp)
	if err == nil && command.RowsAffected() != 1 {
		err = fmt.Errorf("generation %d is not building", id)
	}
	return err
}

func (s PostgreSQLUpdateStore) SetValidating(ctx context.Context, id int64, validation json.RawMessage) error {
	command, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generations SET state='validating',validation=$2::jsonb,validated_at=transaction_timestamp() WHERE id=$1 AND state='building'`, id, validation)
	if err == nil && command.RowsAffected() != 1 {
		err = fmt.Errorf("generation %d is not building", id)
	}
	return err
}

func (s PostgreSQLUpdateStore) Promote(ctx context.Context, generation Generation, validation json.RawMessage) error {
	_, err := s.Pool.Exec(ctx, `SELECT osm_catalog.promote_region_generation($1,$2,$3::name,$4::jsonb)`, generation.RegionID, generation.ID, generation.SchemaName, validation)
	return err
}

func (s PostgreSQLUpdateStore) Fail(ctx context.Context, generation Generation, summary string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generations SET state='failed',failure_summary=$2 WHERE id=$1 AND state IN ('building','validating','evaluating')`, generation.ID, summary)
	return err
}

func (s PostgreSQLUpdateStore) DropBuildSchema(ctx context.Context, schema string) error {
	suffix := strings.TrimPrefix(schema, "osm_build_")
	if suffix == schema {
		return errors.New("invalid build schema")
	}
	id, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != suffix {
		return errors.New("invalid build schema")
	}
	_, err = s.Pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	return err
}

func (s PostgreSQLUpdateStore) ProcessStorageGC(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT id,object_kind,schema_name::text,coalesce(object_name::text,'') FROM osm_catalog.storage_gc WHERE state IN ('queued','dropping','failed') ORDER BY id`)
	if err != nil {
		return err
	}
	type item struct {
		id                   int64
		kind, schema, object string
	}
	var items []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.id, &value.kind, &value.schema, &value.object); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, value := range items {
		if _, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.storage_gc SET state='dropping',failure_summary=NULL WHERE id=$1 AND state IN ('queued','dropping','failed')`, value.id); err != nil {
			return err
		}
		statement := "DROP SCHEMA IF EXISTS " + pgx.Identifier{value.schema}.Sanitize() + " CASCADE"
		if value.kind == "relation" {
			statement = "DROP TABLE IF EXISTS " + pgx.Identifier{value.schema, value.object}.Sanitize()
		}
		if _, err := s.Pool.Exec(ctx, statement); err != nil {
			_, _ = s.Pool.Exec(context.WithoutCancel(ctx), `UPDATE osm_catalog.storage_gc SET state='failed',failure_summary=$2 WHERE id=$1`, value.id, safeFailure(err))
			return err
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.storage_gc SET state='dropped',finished_at=transaction_timestamp() WHERE id=$1`, value.id); err != nil {
			return err
		}
	}
	return nil
}
