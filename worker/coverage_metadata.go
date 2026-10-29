package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func coverageSelectionPolicy(autoAdd bool) string {
	return fmt.Sprintf("route-selection-v1:auto-add=%t", autoAdd)
}

type coverageMatchingDescriptor struct {
	Rules            string  `json:"rules"`
	Sampling         string  `json:"sampling"`
	PathPolicy       string  `json:"pathPolicy"`
	MinimumTraversal float64 `json:"minimumTraversalMeters"`
	Contract         string  `json:"productionMatcherContract"`
}

func productionMatchingDescriptor(minimum float64) coverageMatchingDescriptor {
	return coverageMatchingDescriptor{string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1),
		string(coverage.ExperimentalPathPolicyV82), minimum, coverageProductionContractV1}
}

func readCoverageCatalogFloor(ctx context.Context, db *pgxpool.Pool) (event, revision int64, err error) {
	err = db.QueryRow(ctx, `SELECT last_osm_event_id,catalog_revision FROM app.read_coverage_catalog_observation()`).Scan(&event, &revision)
	return
}

func readCoverageSelectionMetadata(ctx context.Context, db *pgxpool.Pool, account uuid.UUID, job any,
	worker string, lease uuid.UUID, revision *int64, workout uuid.UUID) (coverageHandoff, error) {
	tx, err := beginAccount(ctx, db, account)
	if err != nil {
		return coverageHandoff{}, err
	}
	defer tx.Rollback(ctx)
	var encoded []byte
	if err := tx.QueryRow(ctx, `SELECT app.read_coverage_selection_metadata($1,$2,$3,$4,$5,$6)`,
		account, job, worker, lease, revision, workout).Scan(&encoded); err != nil {
		return coverageHandoff{}, err
	}
	var metadata coverageHandoff
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		return coverageHandoff{}, err
	}
	return metadata, tx.Commit(ctx)
}

func setCoverageSelectionProof(item *coverageHandoff, readiness osm.RouteRegionReadiness, autoAdd bool) {
	item.Readiness, item.Reason, item.Generations = readiness.State, nullableReadinessReason(readiness.Reason), readiness.Regions
	item.CatalogFingerprint = readiness.CatalogFingerprint
	item.CatalogRevision = readiness.CatalogRevision
	item.SelectionFingerprint = readiness.SelectionFingerprint
	item.SelectionPolicy = coverageSelectionPolicy(autoAdd)
}

// Rebinding preserves the exact selected IDs. A changed boundary, configuration,
// or competing active-region presence must instead take the full-route ROI path.
func rebindCoverageSelection(item *coverageHandoff, catalog osm.RouteRegionCatalog, autoAdd bool) bool {
	if item.Readiness == "" || item.SelectionPolicy != coverageSelectionPolicy(autoAdd) ||
		item.SelectionFingerprint == "" || item.SelectionFingerprint != catalog.SelectionFingerprint {
		return false
	}
	for _, region := range item.Generations {
		if catalog.ActiveGenerations[region.RegionID] <= 0 {
			return false
		}
	}
	for i := range item.Generations {
		item.Generations[i].Generation = catalog.ActiveGenerations[item.Generations[i].RegionID]
	}
	item.CatalogRevision, item.CatalogFingerprint = catalog.Revision, catalog.Fingerprint
	return true
}
