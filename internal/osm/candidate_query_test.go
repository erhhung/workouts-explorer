package osm

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCandidateBaselineRemainsFrozen(t *testing.T) {
	if matcherCandidatesSQL != candidateOriginalSQL {
		t.Fatal("candidate fallback changed from the complete original query oracle")
	}
}

type candidateErrorTx struct {
	pgx.Tx
	err     error
	queries int
}

func (tx *candidateErrorTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	tx.queries++
	return nil, tx.err
}

func TestCandidateQueryErrorsNeverRetryBaseline(t *testing.T) {
	for _, queryErr := range []error{context.Canceled, context.DeadlineExceeded, errors.New("aborted SQL transaction")} {
		tx := &candidateErrorTx{err: queryErr}
		_, err := (&MatcherSnapshot{tx: tx}).Candidates(context.Background(), validMatcherObservations(), 1)
		if !errors.Is(err, queryErr) || tx.queries != 1 {
			t.Fatalf("query error must propagate without retry: queries=%d err=%v", tx.queries, err)
		}
	}
}
