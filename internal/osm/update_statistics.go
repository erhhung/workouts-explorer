package osm

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Recursive ANALYZE repairs both inherited parent statistics and leaf statistics,
// including provenance columns added after the earlier pipeline analyses.
var updateStatisticsStatements = []string{
	`ANALYZE osm_canonical.ways (region_id,generation_id,way_id,version)`,
	`ANALYZE osm_canonical.localities (region_id,generation_id,relation_id,relation_version)`,
	`ANALYZE osm_canonical.path_segments (region_id,generation_id,segment_id,source_way_id,source_way_version,logical_path_id,start_graph_node_id,end_graph_node_id)`,
	`ANALYZE osm_catalog.generations (id,region_id,state)`,
}

// ANALYZE can silently skip relations with insufficient privileges. Check every
// leaf as well as the roots so that a successful refresh actually means success.
const updateStatisticsPrivileges = `WITH relations AS (
	SELECT relid FROM pg_partition_tree('osm_canonical.ways')
	UNION SELECT relid FROM pg_partition_tree('osm_canonical.localities')
	UNION SELECT relid FROM pg_partition_tree('osm_canonical.path_segments')
	UNION SELECT 'osm_catalog.generations'::regclass
)
SELECT coalesce(string_agg(relations.relid::regclass::text,', ' ORDER BY relations.relid),'')
FROM relations JOIN pg_class ON pg_class.oid=relations.relid
WHERE NOT (has_table_privilege(relations.relid,'MAINTAIN')
	OR pg_has_role(pg_class.relowner,'USAGE')
	OR pg_has_role((SELECT datdba FROM pg_database WHERE datname=current_database()),'USAGE'))`

func (s PostgreSQLUpdateStore) RefreshStatistics(ctx context.Context) error {
	return refreshUpdateStatistics(ctx, s.Pool)
}

func refreshUpdateStatistics(ctx context.Context, db interface {
	Begin(context.Context) (pgx.Tx, error)
}) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Keep settings local to this maintenance transaction, never to the pool or
	// promotion transaction. These locks allow ordinary SELECT readers to proceed.
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='10min'; SET LOCAL lock_timeout='5s'`); err != nil {
		return err
	}
	var denied string
	if err := tx.QueryRow(ctx, updateStatisticsPrivileges).Scan(&denied); err != nil {
		return err
	}
	if denied != "" {
		return fmt.Errorf("OSM statistics maintenance privilege required for %s", denied)
	}
	for _, statement := range updateStatisticsStatements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return tx.Commit(ctx)
}
