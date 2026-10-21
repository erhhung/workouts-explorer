package worker

import (
	"context"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type coverageFailureStage uint8

const (
	coverageStageRouteRead coverageFailureStage = iota
	coverageStageSnapshotBegin
	coverageStageEvaluate
	coverageStageVerifyGenerations
	coverageStagePrepare
	coverageStagePersist
)

func (s coverageFailureStage) String() string {
	switch s {
	case coverageStageRouteRead:
		return "route_read"
	case coverageStageSnapshotBegin:
		return "snapshot_begin"
	case coverageStageEvaluate:
		return "evaluate"
	case coverageStageVerifyGenerations:
		return "verify_generations"
	case coverageStagePrepare:
		return "prepare"
	case coverageStagePersist:
		return "persist"
	default:
		return "unknown"
	}
}

type coverageFailure struct {
	code, summary, sqlState string
	limitField              string
	limitValue              int
}

// Only known database boundaries can grant transient provenance. Go validation,
// metadata/geometry parsing and arbitrary net.Error implementations cannot.
type coverageDatabaseError struct{ cause error }

func (*coverageDatabaseError) Error() string   { return "Coverage database operation failed." }
func (e *coverageDatabaseError) Unwrap() error { return e.cause }

func coverageDatabaseFailure(err error) error {
	if err == nil {
		return nil
	}
	return &coverageDatabaseError{cause: err}
}

func validCoverageSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, ch := range code {
		if !(ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z') {
			return false
		}
	}
	return true
}

func transientCoverageSQLState(code string) bool {
	switch code {
	case "08003", "08006", "08007", "40001", "40P01", "55P03", "57P01", "57P02", "57P03", "53300":
		return true
	default:
		return false
	}
}

func validCoverageLimitMetadata(field string, limit int) bool {
	if limit < 0 {
		return false
	}
	// Metadata caps only: mirror known parking bounds without widening graph metadata.
	switch field {
	case "graph_node", "graph_edge", "graph_layer":
		return limit <= 1_000_000
	case "parking_repair_accepted_replacements", "parking_repair_states":
		return limit <= 4096
	case "parking_repair_state_bytes":
		return limit <= 512*1024
	case "parking_repair_interned_portions", "parking_repair_suffix_nodes":
		return limit <= 16384
	case "parking_repair_intern_bytes":
		return limit <= 8*1024*1024
	default:
		return false
	}
}

func classifyCoverageFailure(ctx context.Context, stage coverageFailureStage, err error) coverageFailure {
	type entry struct {
		err error
		db  bool
	}
	pending := []entry{{err: err}}
	var permanent, unknown, limit, cancelled, deadline, transient, queryCancelled bool
	var state string
	var limitField string
	var limitValue int
	stateRank := -1
	// Inspect every joined branch. A later permanent/unknown cause must beat an
	// earlier disconnect or incidental expired context; fail closed on cyclic trees.
	for visited := 0; len(pending) > 0; visited++ {
		if visited >= 256 {
			unknown = true
			break
		}
		item := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if item.err == nil {
			continue
		}
		if boundary, ok := item.err.(*coverageDatabaseError); ok {
			pending = append(pending, entry{boundary.cause, true})
			continue
		}
		recognized := false
		if sql, ok := item.err.(interface{ SQLState() string }); ok {
			recognized = true
			code := sql.SQLState()
			if !item.db || !validCoverageSQLState(code) {
				unknown = true
			} else {
				rank := 0
				switch {
				case code == "57014":
					queryCancelled, rank = true, 1
				case transientCoverageSQLState(code):
					transient = true
				default:
					permanent, rank = true, 2
				}
				if rank > stateRank || rank == stateRank && code < state {
					state, stateRank = code, rank
				}
			}
		}
		switch value := item.err.(type) {
		case *coverage.OSMEvaluationLimitError:
			limit, recognized = true, true
			if value != nil {
				field := ""
				switch value.Field {
				case "graph node":
					field = "graph_node"
				case "graph edge":
					field = "graph_edge"
				case "graph layer":
					field = "graph_layer"
				case "parking repair accepted replacements":
					field = "parking_repair_accepted_replacements"
				case "parking repair states":
					field = "parking_repair_states"
				case "parking repair state bytes":
					field = "parking_repair_state_bytes"
				case "parking repair interned portions":
					field = "parking_repair_interned_portions"
				case "parking repair intern bytes":
					field = "parking_repair_intern_bytes"
				case "parking repair suffix nodes":
					field = "parking_repair_suffix_nodes"
				}
				if validCoverageLimitMetadata(field, value.Limit) && (limitField == "" || field < limitField || field == limitField && value.Limit < limitValue) {
					limitField, limitValue = field, value.Limit
				}
			}
		case *osm.MatcherBoundsError, *osm.MatcherOverflowError, *routepipeline.LimitError:
			limit, recognized = true, true
		case pgx.ScanArgError, *pgx.ScanArgError:
			permanent, recognized = true, true
		}
		switch item.err {
		case context.Canceled:
			cancelled, recognized = true, true
		case context.DeadlineExceeded:
			deadline, recognized = true, true
		case io.EOF, io.ErrUnexpectedEOF, pgconn.ErrConnClosed, net.ErrClosed, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE:
			recognized = true
			if item.db {
				transient = true
			} else {
				unknown = true
			}
		}
		var children []error
		switch wrapped := item.err.(type) {
		case interface{ Unwrap() []error }:
			children = wrapped.Unwrap()
		case interface{ Unwrap() error }:
			if child := wrapped.Unwrap(); child != nil {
				children = []error{child}
			}
		}
		if !recognized && len(children) == 0 {
			unknown = true
		}
		for _, child := range children {
			pending = append(pending, entry{child, item.db})
		}
	}
	code, summary := "coverage-osm-unavailable", "Coverage map data could not be processed."
	if stage == coverageStageRouteRead {
		code, summary = "coverage-route-read-failed", "Coverage route input could not be read."
	} else if stage == coverageStagePersist {
		code, summary = "coverage-persist-failed", "Coverage results could not be persisted."
	}
	switch {
	case limit:
		code, summary = "coverage-route-too-large", "Coverage route exceeded a processing limit."
	case permanent || unknown || cancelled || ctx.Err() == context.Canceled:
	case deadline || ctx.Err() == context.DeadlineExceeded:
		code, summary = "coverage-route-timeout", "Coverage matching timed out."
	case transient && !queryCancelled:
		code, summary = "coverage-transient-failed", "Coverage processing was interrupted by a transient database failure."
	}
	return coverageFailure{code: code, summary: summary, sqlState: state, limitField: limitField, limitValue: limitValue}
}

func (r *CoverageRunner) failClassifiedDetached(claim coverageClaim, started time.Time, stage coverageFailureStage, failure coverageFailure) error {
	r.logCoverageFailure(claim.jobID, stage, failure)
	return r.failDetached(claim, started, failure.code, failure.summary)
}

func (r *CoverageRunner) logCoverageFailure(job uuid.UUID, stage coverageFailureStage, failure coverageFailure) {
	state := failure.sqlState
	if !validCoverageSQLState(state) {
		state = ""
	}
	code := failure.code
	switch code {
	case "coverage-route-invalid", "coverage-route-read-failed", "coverage-route-timeout", "coverage-route-too-large",
		"coverage-osm-unavailable", "coverage-persist-failed", "coverage-transient-failed":
	default:
		code = "coverage-osm-unavailable"
	}
	// Never attach the error, summary, SQL/query/driver fields or route data.
	fields := []any{"job_id", job, "stage", stage.String(), "sqlstate", state, "failure_code", code}
	if code == "coverage-route-too-large" && validCoverageLimitMetadata(failure.limitField, failure.limitValue) {
		fields = append(fields, "limit_field", failure.limitField, "limit", failure.limitValue)
	}
	r.logger.Warn("coverage route failure", fields...)
}

type coverageDatabaseSnapshot struct {
	routepipeline.ProductionSnapshot
}

func (s coverageDatabaseSnapshot) Candidates(ctx context.Context, observations []osm.MatcherObservation, n int) ([]osm.MatcherCandidate, error) {
	value, err := s.ProductionSnapshot.Candidates(ctx, observations, n)
	return value, coverageDatabaseFailure(err)
}
func (s coverageDatabaseSnapshot) IncidentEdges(ctx context.Context, nodes []uuid.UUID, n int) ([]osm.MatcherIncidentEdge, error) {
	value, err := s.ProductionSnapshot.IncidentEdges(ctx, nodes, n)
	return value, coverageDatabaseFailure(err)
}
func (s coverageDatabaseSnapshot) ClipPortions(ctx context.Context, refs []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	value, err := s.ProductionSnapshot.ClipPortions(ctx, refs)
	return value, coverageDatabaseFailure(err)
}
func (s coverageDatabaseSnapshot) Generations(ctx context.Context) ([]osm.MatcherGeneration, error) {
	value, err := s.ProductionSnapshot.Generations(ctx)
	return value, coverageDatabaseFailure(err)
}
func (s coverageDatabaseSnapshot) UnavailableRegions(ctx context.Context, observations []osm.MatcherObservation) ([]osm.MatcherUnavailableRegion, error) {
	value, err := s.ProductionSnapshot.UnavailableRegions(ctx, observations)
	return value, coverageDatabaseFailure(err)
}
func (s coverageDatabaseSnapshot) CopySegments(ctx context.Context, refs []osm.MatcherSegmentRef) ([]osm.MatcherSegmentCopy, error) {
	value, err := s.ProductionSnapshot.CopySegments(ctx, refs)
	return value, coverageDatabaseFailure(err)
}
