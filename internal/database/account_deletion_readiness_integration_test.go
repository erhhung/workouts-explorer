package database

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
)

func TestAccountDeletionReadinessIntegration(t *testing.T) {
	if os.Getenv("M10_ADMIN_TEST_DISPOSABLE") != "1" {
		t.Skip("owned disposable database required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, os.Getenv("API_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for name, query := range map[string]string{"core": runtimeReadinessSQL, "deferred": runtimeDeferredReadinessSQL, "cohort": runtimeCohortReadinessSQL, "display": runtimeCoverageDisplayReadinessSQL, "canonical": runtimeCanonicalCoverageDisplayReadinessSQL, "phase": runtimeIngestPhaseReadinessSQL, "tiles": runtimeTileReadinessSQL, "lod": runtimeTileLODReadinessSQL, "metadata": runtimeCoverageMetadataReadinessSQL, "gaps": runtimeRouteGapReadinessSQL, "checks": runtimeSourceCheckReadinessSQL} {
		var good bool
		var args []any
		if strings.Contains(query, "$1") {
			args = []any{SupportedSchemaVersion}
		}
		if err := db.QueryRow(ctx, "SELECT ("+query+")", args...).Scan(&good); err != nil || !good {
			t.Errorf("%s readiness good=%t err=%v", name, good, err)
		}
	}
}
