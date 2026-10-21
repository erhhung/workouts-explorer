package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCoverageFailureTransientBoundaries(t *testing.T) {
	for _, cause := range []error{
		&pgconn.PgError{Code: "08003"}, &pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "08007"},
		&pgconn.PgError{Code: "40001"}, &pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "55P03"},
		&pgconn.PgError{Code: "57P01"}, &pgconn.PgError{Code: "57P02"}, &pgconn.PgError{Code: "57P03"}, &pgconn.PgError{Code: "53300"},
		io.EOF, io.ErrUnexpectedEOF, pgconn.ErrConnClosed, net.ErrClosed,
		&net.OpError{Op: "read", Err: &net.OpError{Op: "read", Err: syscall.ECONNRESET}}, syscall.EPIPE, syscall.ECONNABORTED,
	} {
		for _, stage := range []coverageFailureStage{coverageStageRouteRead, coverageStageSnapshotBegin, coverageStageEvaluate, coverageStageVerifyGenerations, coverageStagePrepare, coverageStagePersist} {
			failure := classifyCoverageFailure(context.Background(), stage, coverageDatabaseFailure(fmt.Errorf("private-wrapper-canary: %w", cause)))
			if failure.code != "coverage-transient-failed" {
				t.Fatalf("verified boundary %T at %s: code=%s", cause, stage, failure.code)
			}
			if untrusted := classifyCoverageFailure(context.Background(), stage, cause); untrusted.code == "coverage-transient-failed" {
				t.Fatalf("unverified %T granted transient provenance", cause)
			}
		}
	}
}

func TestCoverageFailurePermanentPrecedenceAndUnknowns(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	causes := []error{
		errors.New("private-invalid-metadata-canary"),
		&tls.RecordHeaderError{Msg: "private-TLS-config-canary"},
		&net.DNSError{Name: "private-host-canary.invalid", Err: "private-canary", IsNotFound: true},
		&net.DNSError{Name: "private-host-canary.invalid", Err: "private-canary", IsTimeout: true},
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		pgx.ScanArgError{ColumnIndex: 0, FieldName: "private-canary", Err: io.EOF},
	}
	for _, code := range []string{"28P01", "28000", "42501", "42P01", "42601", "22023", "53100", "53200", "53400", "XX000", "08004", "08P01", "private-state-canary"} {
		causes = append(causes, &pgconn.PgError{Code: code, Message: "private-message-canary", Detail: "private-query-canary"})
	}
	for _, cause := range causes {
		for _, joined := range []error{cause, errors.Join(cause, io.EOF), errors.Join(io.EOF, &pgconn.PgError{Code: "08006"}, context.DeadlineExceeded, cause)} {
			for _, ctx := range []context.Context{context.Background(), expired} {
				for stage, want := range map[coverageFailureStage]string{
					coverageStageRouteRead: "coverage-route-read-failed", coverageStageEvaluate: "coverage-osm-unavailable", coverageStagePersist: "coverage-persist-failed",
				} {
					failure := classifyCoverageFailure(ctx, stage, coverageDatabaseFailure(joined))
					if failure.code != want {
						t.Fatalf("permanent %T at %s became %s, want %s", cause, stage, failure.code, want)
					}
					if sql, ok := cause.(*pgconn.PgError); ok && validCoverageSQLState(sql.Code) && failure.sqlState != sql.Code {
						t.Fatalf("permanent SQLSTATE lost to another branch: got=%s want=%s", failure.sqlState, sql.Code)
					}
				}
			}
		}
	}
	limit := &routepipeline.LimitError{Field: "private-limit-canary", Limit: 1}
	if failure := classifyCoverageFailure(expired, coverageStageEvaluate, coverageDatabaseFailure(errors.Join(io.EOF, limit))); failure.code != "coverage-route-too-large" {
		t.Fatalf("limit overridden by timeout/disconnect: %s", failure.code)
	}
	if failure := classifyCoverageFailure(context.Background(), coverageStageEvaluate, coverageDatabaseFailure(&pgconn.PgError{Code: "57014"})); failure.code != "coverage-osm-unavailable" {
		t.Fatal("unexplained PostgreSQL user cancellation became a retryable timeout")
	}
	if failure := classifyCoverageFailure(expired, coverageStageEvaluate, coverageDatabaseFailure(&pgconn.PgError{Code: "57014"})); failure.code != "coverage-route-timeout" {
		t.Fatal("actual operation deadline lost timeout identity")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if failure := classifyCoverageFailure(cancelled, coverageStageEvaluate, coverageDatabaseFailure(io.EOF)); failure.code == "coverage-transient-failed" {
		t.Fatal("cancelled operation became automatically retryable")
	}
}

func TestCoverageFailureTypedGraphLimitsAndSafeDetails(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, test := range []struct {
		field, want string
		limit       int
	}{
		{"graph node", "graph_node", 2048}, {"graph edge", "graph_edge", 4096},
		{"graph layer", "graph_layer", 64}, {"graph layer", "graph_layer", 0},
		{"graph node", "graph_node", 1_000_000},
		{"private-field-canary postgresql://private-host-canary", "", 2048},
		{"parking repair state bytes; SELECT 'private-SQL-canary'", "", 512 * 1024},
		{"graph node", "", -1}, {"graph node", "", 1_000_001},
	} {
		t.Run(test.field+"/"+fmt.Sprint(test.limit), func(t *testing.T) {
			limit := &coverage.OSMEvaluationLimitError{Field: test.field, Limit: test.limit}
			for _, ctx := range []context.Context{context.Background(), expired} {
				for _, cause := range []error{limit, fmt.Errorf("private-wrapper-canary: %w", limit), errors.Join(coverageDatabaseFailure(io.EOF), context.DeadlineExceeded, limit)} {
					failure := classifyCoverageFailure(ctx, coverageStageEvaluate, cause)
					if failure.code != "coverage-route-too-large" || failure.sqlState != "" || failure.limitField != test.want {
						t.Fatalf("typed graph limit lost permanent classification/details: code=%s state=%s field=%s", failure.code, failure.sqlState, failure.limitField)
					}
					var logs bytes.Buffer
					runner := NewCoverageRunner(nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
					runner.logCoverageFailure(uuid.New(), coverageStageEvaluate, failure)
					var record map[string]any
					if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
						t.Fatal(err)
					}
					if test.want != "" {
						if record["limit_field"] != test.want || record["limit"] != float64(test.limit) {
							t.Fatal("safe graph limit metadata missing")
						}
					} else if record["limit_field"] != nil || record["limit"] != nil {
						t.Fatal("unvalidated graph limit metadata was logged")
					}
					if strings.Contains(logs.String(), "canary") || strings.Contains(logs.String(), "postgresql://") {
						t.Fatal("typed graph limit leaked its field or native error message")
					}
				}
			}
		})
	}
}

func TestCoverageFailureTypedParkingLimitsAndSafeDetails(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, test := range []struct {
		field, enum string
		maximum     int
	}{
		{"parking repair accepted replacements", "parking_repair_accepted_replacements", 4096},
		{"parking repair states", "parking_repair_states", 4096},
		{"parking repair state bytes", "parking_repair_state_bytes", 512 * 1024},
		{"parking repair interned portions", "parking_repair_interned_portions", 16384},
		{"parking repair intern bytes", "parking_repair_intern_bytes", 8 * 1024 * 1024},
		{"parking repair suffix nodes", "parking_repair_suffix_nodes", 16384},
	} {
		for _, value := range []int{-1, 0, 1, test.maximum, test.maximum + 1} {
			t.Run(test.enum+"/"+fmt.Sprint(value), func(t *testing.T) {
				want := test.enum
				if value < 0 || value > test.maximum {
					want = ""
				}
				parking := &coverage.ParkingRepairLimitError{
					Field: test.field, MaxAcceptedReplacements: value, MaxStates: value, MaxStateBytes: value,
					MaxInternedPortions: value, MaxInternBytes: value, MaxSuffixNodes: value,
					States: 7, ProjectedStates: 8, StateBytes: 32, AttemptedKeyBytes: 16, ProjectedStateBytes: 48,
				}
				for _, cause := range []error{
					&coverage.OSMEvaluationLimitError{Field: test.field, Limit: value},
					parking, fmt.Errorf("private-wrapper-canary: %w", parking),
					errors.Join(coverageDatabaseFailure(io.EOF), context.DeadlineExceeded, parking),
					errors.Join(parking, &pgconn.PgError{Code: "42501", Message: "private-SQL-canary"}),
					errors.Join(errors.New("private-unknown-canary"), parking),
				} {
					for _, ctx := range []context.Context{context.Background(), expired} {
						failure := classifyCoverageFailure(ctx, coverageStageEvaluate, cause)
						if failure.code != "coverage-route-too-large" || failure.sqlState != "" || failure.limitField != want {
							t.Fatalf("parking limit lost classification/details: %+v", failure)
						}
						if want == "" && failure.limitValue != 0 || want != "" && failure.limitValue != value {
							t.Fatalf("invalid classified numeric metadata: %+v", failure)
						}
						// Revalidate independently of classification, including forged numeric metadata.
						for _, metadata := range []coverageFailure{failure, {code: "coverage-route-too-large", limitField: test.enum, limitValue: value}} {
							var logs bytes.Buffer
							runner := NewCoverageRunner(nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
							runner.logCoverageFailure(uuid.New(), coverageStageEvaluate, metadata)
							var record map[string]any
							if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
								t.Fatal(err)
							}
							if want != "" {
								if record["limit_field"] != want || record["limit"] != float64(value) {
									t.Fatalf("safe parking metadata missing: %v", record)
								}
							} else if record["limit_field"] != nil || record["limit"] != nil {
								t.Fatal("unvalidated parking metadata was logged")
							}
							for key := range record {
								switch key {
								case "time", "level", "msg", "job_id", "stage", "sqlstate", "failure_code", "limit_field", "limit":
								default:
									t.Fatalf("unexpected failure log field: %s", key)
								}
							}
							if strings.Contains(logs.String(), "canary") || strings.Contains(logs.String(), "parking repair tracking") {
								t.Fatal("parking failure log leaked native details")
							}
						}
					}
				}
			})
		}
	}
}

func TestCoverageFailureStructuredLogRedaction(t *testing.T) {
	var output bytes.Buffer
	runner := NewCoverageRunner(nil, nil, slog.New(slog.NewJSONHandler(&output, nil)))
	for stage, name := range map[coverageFailureStage]string{
		coverageStageRouteRead: "route_read", coverageStageSnapshotBegin: "snapshot_begin", coverageStageEvaluate: "evaluate",
		coverageStageVerifyGenerations: "verify_generations", coverageStagePrepare: "prepare", coverageStagePersist: "persist", 255: "unknown",
	} {
		output.Reset()
		err := coverageDatabaseFailure(errors.Join(io.EOF, &pgconn.PgError{Code: "42501", Message: "postgresql://private-host-canary/private-query-canary", Detail: "GPS-canary password-canary /private/source-canary"}))
		failure := classifyCoverageFailure(context.Background(), stage, err)
		runner.logCoverageFailure(uuid.New(), stage, failure)
		var record map[string]any
		if err := json.Unmarshal(output.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["stage"] != name || record["sqlstate"] != "42501" || record["error"] != nil {
			t.Fatalf("invalid structured fields: %v", record)
		}
		if strings.Contains(output.String(), "canary") || strings.Contains(output.String(), "postgresql://") {
			t.Fatal("failure log leaked original details")
		}
	}
	output.Reset()
	runner.logCoverageFailure(uuid.New(), 255, coverageFailure{code: "private-code-canary", summary: "private-summary-canary", sqlState: "42501\nprivate-state-canary"})
	if strings.Contains(output.String(), "canary") || !strings.Contains(output.String(), `"sqlstate":""`) {
		t.Fatal("unvalidated failure metadata entered logs")
	}
	for _, field := range []string{"private-limit-canary", "parking_repair_state_bytes; SELECT 'private-SQL-canary'", "parking repair state bytes"} {
		output.Reset()
		runner.logCoverageFailure(uuid.New(), coverageStageEvaluate, coverageFailure{code: "coverage-route-too-large", limitField: field, limitValue: 2048})
		if strings.Contains(output.String(), "canary") || strings.Contains(output.String(), `"limit"`) {
			t.Fatal("failure metadata bypassed the limit enum")
		}
	}
}

type coverageFailureSnapshotFixture struct {
	routepipeline.ProductionSnapshot
	err error
}

func (s coverageFailureSnapshotFixture) Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error) {
	return nil, s.err
}
func (s coverageFailureSnapshotFixture) IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error) {
	return nil, s.err
}
func (s coverageFailureSnapshotFixture) ClipPortions(context.Context, []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	return nil, s.err
}
func (s coverageFailureSnapshotFixture) Generations(context.Context) ([]osm.MatcherGeneration, error) {
	return nil, s.err
}
func (s coverageFailureSnapshotFixture) UnavailableRegions(context.Context, []osm.MatcherObservation) ([]osm.MatcherUnavailableRegion, error) {
	return nil, s.err
}
func (s coverageFailureSnapshotFixture) CopySegments(context.Context, []osm.MatcherSegmentRef) ([]osm.MatcherSegmentCopy, error) {
	return nil, s.err
}

func TestCoverageFailureSnapshotBoundaries(t *testing.T) {
	snapshot := coverageDatabaseSnapshot{coverageFailureSnapshotFixture{err: &pgconn.PgError{Code: "08006"}}}
	ctx := context.Background()
	_, a := snapshot.Candidates(ctx, nil, 1)
	_, b := snapshot.IncidentEdges(ctx, nil, 1)
	_, c := snapshot.ClipPortions(ctx, nil)
	_, d := snapshot.Generations(ctx)
	_, e := snapshot.UnavailableRegions(ctx, nil)
	_, f := snapshot.CopySegments(ctx, nil)
	for _, err := range []error{a, b, c, d, e, f} {
		if failure := classifyCoverageFailure(ctx, coverageStageEvaluate, err); failure.code != "coverage-transient-failed" {
			t.Fatalf("snapshot boundary lost provenance: %s", failure.code)
		}
	}
}
