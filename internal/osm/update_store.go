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
	_, err := s.Pool.Exec(ctx, `UPDATE osm_catalog.generations SET state='failed',failure_summary=$2 WHERE id=$1 AND state IN ('building','validating')`, generation.ID, summary)
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
