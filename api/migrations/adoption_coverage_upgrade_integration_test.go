package migrations_test

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
)

func adoptionCoverageUpgradeCompactFunctions(t *testing.T, ctx context.Context, db *pgx.Conn, provider *goose.Provider) {
	t.Helper()
	type replacement struct{ original, compact string }
	functions := []struct {
		signature    string
		replacements []replacement
	}{
		{"app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)", []replacement{
			{"WITH points AS MATERIALIZED (", "with POINTS as materialized("},
			{"FROM encoded point;", "from ENCODED POINT ;"},
		}},
		{"app.claim_coverage_reconciliation(text,uuid,interval,integer)", []replacement{
			{"AND reconciliation.next_scan_at <= transaction_timestamp()", "aNd reconciliation . next_scan_at<=TRANSACTION_TIMESTAMP ( )"},
			{"IF candidate.active_revision IS DISTINCT FROM candidate.desired_revision OR candidate.completed_revision >= candidate.desired_revision THEN",
				"if CANDIDATE . ACTIVE_REVISION is\tdistinct from candidate.desired_revision or candidate . completed_revision>=candidate.desired_revision then"},
		}},
		{"app.claim_next_coverage_route(text,uuid,interval,integer)", []replacement{
			{"runtime_version IS NULL OR runtime_version < 23", "RUNTIME_VERSION is null or runtime_version<23"},
		}},
		{"app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)", []replacement{
			{"AND workout.deletion_requested_at IS NULL", "and WORKOUT . DELETION_REQUESTED_AT is\nnull"},
		}},
		{"app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)", []replacement{
			{"IF NOT needs_match THEN RETURN true; END IF;", "if not NEEDS_MATCH then\nreturn TRUE ;end if ;"},
		}},
	}
	signatures := make([]string, len(functions))
	for i, function := range functions {
		signatures[i] = function.signature
	}
	const contractsSQL = `SELECT jsonb_object_agg(expected.signature,jsonb_build_object(
		'oid',function.oid,'owner',function.proowner,'acl',function.proacl,'config',function.proconfig,
		'definer',function.prosecdef,'volatility',function.provolatile,'strict',function.proisstrict,
		'arguments',function.proargnames,'defaults',pg_get_expr(function.proargdefaults,0)))
		FROM unnest($1::text[]) expected(signature) JOIN pg_proc function ON function.oid=to_regprocedure(expected.signature)`
	var before, after []byte
	if err := db.QueryRow(ctx, contractsSQL, signatures).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var compactClaim, compactDue string
	for _, function := range functions {
		var original string
		if err := db.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure)`, function.signature).Scan(&original); err != nil {
			t.Fatal(err)
		}
		compact := original
		for _, change := range function.replacements {
			if strings.Count(compact, change.original) != 1 {
				t.Fatalf("public fixture anchor is not unique in %s: %s", function.signature, change.original)
			}
			compact = strings.Replace(compact, change.original, change.compact, 1)
		}
		foldWhitespaceAndCase := func(source string) string {
			return strings.ToLower(strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, source))
		}
		if original == compact || foldWhitespaceAndCase(original) != foldWhitespaceAndCase(compact) {
			t.Fatalf("compact fixture changed more than whitespace/case in %s", function.signature)
		}
		if _, err := db.Exec(ctx, compact); err != nil {
			t.Fatalf("format public fixture %s: %v", function.signature, err)
		}
		if strings.HasPrefix(function.signature, "app.claim_coverage_reconciliation(") {
			compactClaim, compactDue = compact, function.replacements[0].compact
		}
	}
	if err := db.QueryRow(ctx, contractsSQL, signatures).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatalf("formatting changed function identity/owner/ACL/search_path/attributes: err=%v", err)
	}
	// Unsupported or ambiguous definitions must abort the entire migration. These
	// isolated negative fixtures are restored before the formatting-only upgrade.
	for _, scenario := range []string{"missing", "duplicate"} {
		if !t.Run("Reject_"+scenario+"_anchor", func(t *testing.T) {
			mutated := strings.Replace(compactClaim, compactDue, "AND (reconciliation.next_scan_at<=transaction_timestamp())", 1)
			count := "0"
			if scenario == "duplicate" {
				if strings.Count(compactClaim, "\nBEGIN\n") != 1 {
					t.Fatal("public claim fixture BEGIN is not unique")
				}
				mutated = strings.Replace(compactClaim, "\nBEGIN\n", "\n-- "+compactDue+"\nBEGIN\n", 1)
				count = "2"
			}
			if _, err := db.Exec(ctx, mutated); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := db.Exec(ctx, compactClaim); err != nil {
					t.Error("restore formatting-only claim fixture:", err)
				}
			}()
			_, err := provider.UpTo(ctx, 26)
			bootstrapSQLState(t, err, "P0001")
			if !strings.Contains(err.Error(), "reconciliation claim due anchor must occur exactly once (found "+count+")") {
				t.Fatalf("unexpected bounded-anchor failure: %v", err)
			}
			var rolledBack bool
			if err := db.QueryRow(ctx, `SELECT to_regclass('app.adopted_workout_coverage_refreshes') IS NULL
				AND to_regprocedure('app.adopted_coverage_route_digest(uuid,uuid)') IS NULL
				AND (SELECT max(version_id)=25 FROM public.goose_db_version WHERE is_applied)`).Scan(&rolledBack); err != nil || !rolledBack {
				t.Fatalf("anchor rejection did not roll back migration 26: rolledBack=%v err=%v", rolledBack, err)
			}
		}) {
			t.Fatal("unsafe dynamic-anchor acceptance")
		}
	}
	if _, err := provider.UpTo(ctx, 26); err != nil {
		t.Fatal("upgrade compact public function definitions:", err)
	}
	if err := db.QueryRow(ctx, contractsSQL, signatures).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatalf("migration changed function identity/owner/ACL/search_path/attributes: err=%v", err)
	}
}
