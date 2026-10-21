package readerplacement

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type stateError struct{ code string }

func (e *stateError) Error() string    { return "OSM reader operation failed (SQLSTATE " + e.code + ")" }
func (e *stateError) SQLState() string { return e.code }

func sanitize(err error) error {
	if err == nil {
		return nil
	}
	code := errorSQLState(err)
	if errors.Is(err, ErrCleanupUnconfirmed) {
		if code != "" && !transientSQLState(code) {
			return errors.Join(ErrCleanupUnconfirmed, &stateError{code})
		}
		return ErrCleanupUnconfirmed
	}
	if code != "" && !transientSQLState(code) {
		return &stateError{code}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if unclassifiedError(err) {
		return errors.New("OSM reader operation failed")
	}
	for _, sentinel := range []error{context.DeadlineExceeded, osm.ErrMatcherGenerationChanged, osm.ErrMatcherReplicaNotReady} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	if code != "" {
		return &stateError{code}
	}
	return osm.ErrMatcherReplicaNotReady
}

func permanent(err error) bool {
	if err == nil {
		return false
	}
	if code := errorSQLState(err); code != "" && !transientSQLState(code) {
		return true
	}
	return errors.Is(err, ErrCleanupUnconfirmed) || errors.Is(err, osm.ErrMatcherGenerationChanged) ||
		!errors.Is(err, context.Canceled) && unclassifiedError(err)
}

func transientSQLState(code string) bool {
	return len(code) == 5 && (code[:2] == "08" || code == "57P01" || code == "57P02" || code == "57P03")
}

func validSQLState(code string) bool {
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

// errors.As finds only the first matching branch of errors.Join. Inspect every
// branch so a transient state cannot hide a later permission/authentication error.
func errorSQLState(err error) string {
	var code string
	if state, ok := err.(interface{ SQLState() string }); ok && validSQLState(state.SQLState()) {
		code = state.SQLState()
		if !transientSQLState(code) {
			return code
		}
	}
	var children []error
	switch err := err.(type) {
	case interface{ Unwrap() []error }:
		children = err.Unwrap()
	case interface{ Unwrap() error }:
		children = []error{err.Unwrap()}
	}
	for _, child := range children {
		if childCode := errorSQLState(child); childCode != "" {
			if !transientSQLState(childCode) {
				return childCode
			}
			code = childCode
		}
	}
	return code
}

func unclassifiedError(err error) bool {
	switch err {
	case nil, context.Canceled, context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF,
		pgconn.ErrConnClosed, osm.ErrMatcherGenerationChanged, osm.ErrMatcherReplicaNotReady:
		return false
	}
	if state, ok := err.(interface{ SQLState() string }); ok && validSQLState(state.SQLState()) {
		return false
	}
	if _, ok := err.(net.Error); ok {
		return false
	}
	switch err := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range err.Unwrap() {
			if unclassifiedError(child) {
				return true
			}
		}
		return false
	case interface{ Unwrap() error }:
		if child := err.Unwrap(); child != nil {
			return unclassifiedError(child)
		}
	}
	return true
}

// Keep the configured query/connection tracer, but strip inputs and raw errors
// before dispatching. Placement role/head probes use unnamed low-level protocol.
type safeTracer struct{ query pgx.QueryTracer }

func (t *safeTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if t.query == nil {
		return ctx
	}
	data.SQL, data.Args = "OSM reader query", nil
	return t.query.TraceQueryStart(ctx, nil, data)
}

func (t *safeTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if t.query != nil {
		data.Err = sanitize(data.Err)
		t.query.TraceQueryEnd(ctx, nil, data)
	}
}

func (t *safeTracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	if tracer, ok := t.query.(pgx.ConnectTracer); ok {
		// ConnString retains the original private URI even in Copy.
		data.ConnConfig = nil
		return tracer.TraceConnectStart(ctx, data)
	}
	return ctx
}

func (t *safeTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	if tracer, ok := t.query.(pgx.ConnectTracer); ok {
		data.Err, data.Conn = sanitize(data.Err), nil
		tracer.TraceConnectEnd(ctx, data)
	}
}
