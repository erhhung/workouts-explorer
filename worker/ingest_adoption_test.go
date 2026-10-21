package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/workouttimezone"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type adoptionRow func(...any) error

func (row adoptionRow) Scan(dest ...any) error { return row(dest...) }

type adoptionStatement struct {
	sql  string
	args []any
}

type adoptionTx struct {
	pgx.Tx
	outcome    string
	workoutID  uuid.UUID
	priorHash  []byte
	resolveErr error
	outboxErr  error
	statements []adoptionStatement
}

func (tx *adoptionTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	tx.statements = append(tx.statements, adoptionStatement{sql, args})
	return adoptionRow(func(dest ...any) error {
		values := []any{true}
		switch {
		case strings.Contains(sql, "resolve_ingest_workout_identity"):
			if tx.resolveErr != nil {
				return tx.resolveErr
			}
			values = []any{tx.workoutID, tx.priorHash, tx.outcome}
		case strings.Contains(sql, "INSERT INTO app.workout_types"):
			values = []any{args[0]}
		}
		if len(dest) != len(values) {
			return fmt.Errorf("unexpected scan arity: %d", len(dest))
		}
		for index, value := range values {
			reflect.ValueOf(dest[index]).Elem().Set(reflect.ValueOf(value))
		}
		return nil
	})
}

func (tx *adoptionTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, adoptionStatement{sql, args})
	if strings.Contains(sql, "record_adopted_workout_coverage_refresh") {
		return pgconn.CommandTag{}, tx.outboxErr
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestIngestAdoptionWorkoutOutcomes(t *testing.T) {
	document, err := healthautoexport.Parse(strings.NewReader(minimalExport))
	if err != nil {
		t.Fatal(err)
	}
	workout := document.Workouts[0]
	for _, test := range []struct {
		name, outcome, kind string
		unchanged, raw      bool
	}{
		{"created", "created", "created", false, true},
		{"same source changed", "existing", "updated", false, true},
		{"same source unchanged", "existing", "matched_unchanged", true, false},
		{"adopt changed", "adopted", "adopted", false, true},
		{"adopt unchanged", "adopted", "adopted", true, false},
		{"live owner", "skipped_live_owner", "skipped_live_owner", false, false},
		{"suppressed", "suppressed", "suppressed", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &adoptionTx{outcome: test.outcome, workoutID: uuid.New()}
			if test.unchanged {
				tx.priorHash = workout.ContentSHA256[:]
			}
			job := claimedJob{id: uuid.New(), accountID: uuid.New(), lease: uuid.New()}
			sourceID, fileID := uuid.New(), uuid.New()
			runner := &Runner{workerID: "adoption-worker"}
			kind, err := runner.persistWorkout(context.Background(), tx, job, sourceID, fileID, workout, workouttimezone.Resolution{})
			if err != nil || kind != test.kind {
				t.Fatalf("kind=%q err=%v", kind, err)
			}
			first := tx.statements[0]
			if !strings.Contains(first.sql, "resolve_ingest_workout_identity") || first.args[0] != job.id ||
				first.args[1] != runner.workerID || first.args[2] != job.lease || first.args[3] != fileID ||
				*first.args[5].(*string) != workout.ProviderID || first.args[6] != (*string)(nil) || len(first.args[7].([]byte)) != 0 {
				t.Fatal("identity helper arguments do not match the fenced incoming workout")
			}
			raw, event, outbox, ownership := false, false, false, false
			for _, statement := range tx.statements[1:] {
				sql, args := statement.sql, statement.args
				if strings.Contains(sql, "DELETE FROM app.workout_") {
					raw = true
				}
				if strings.Contains(sql, "UPDATE app.workouts SET") {
					ownership = strings.Contains(sql, "source_id=") && strings.Contains(sql, "source_file_id=") && args[0] == tx.workoutID
				}
				if strings.Contains(sql, "INSERT INTO app.workout_import_events") {
					event = true
					if args[2] != sourceID || args[3] != tx.workoutID || args[4] != fileID || args[5] != job.id || args[6] != test.kind ||
						!bytes.Equal(args[7].([]byte), workout.ContentSHA256[:]) {
						t.Fatal("event lost incoming provenance or real content digest")
					}
				}
				if strings.Contains(sql, "record_adopted_workout_coverage_refresh") {
					if !event || args[0] != job.id || args[1] != runner.workerID || args[2] != job.lease || args[3] != tx.workoutID {
						t.Fatal("outbox must follow the adopted event in the same transaction")
					}
					outbox = true
				}
				if strings.Contains(sql, "enqueue_coverage") || strings.Contains(sql, "initialize_workout_coverage") {
					t.Fatal("file transaction acquired coverage state")
				}
				if strings.Contains(sql, "replace_workout_split_summary") {
					t.Fatal("empty route has no summary row for split replacement")
				}
			}
			if raw != test.raw || event != (test.outcome != "suppressed") || outbox != (test.outcome == "adopted") ||
				test.outcome == "adopted" && !ownership {
				t.Fatalf("raw/event/outbox/ownership=%t/%t/%t/%t", raw, event, outbox, ownership)
			}
			if test.outcome == "suppressed" && len(tx.statements) != 1 || test.outcome == "skipped_live_owner" && len(tx.statements) != 2 {
				t.Fatal("blocked/suppressed identity mutated workout or type state")
			}
		})
	}
}

func TestIngestAdoptionFallbackAndTransientErrors(t *testing.T) {
	document, err := healthautoexport.Parse(strings.NewReader(strings.Replace(minimalExport, `"id":"provider-1",`, "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	workout := document.Workouts[0]
	for _, code := range []string{"55P03", "40P01", "40001"} {
		t.Run(code, func(t *testing.T) {
			cause := &pgconn.PgError{Code: code, Message: "synthetic lock failure"}
			tx := &adoptionTx{resolveErr: cause}
			_, err := (&Runner{}).persistWorkout(context.Background(), tx, claimedJob{}, uuid.New(), uuid.New(), workout, workouttimezone.Resolution{})
			if !errors.Is(err, cause) || !errors.Is(ingestCommitError(fmt.Errorf("wrapped: %w", err)), errLeaseLost) || len(tx.statements) != 1 {
				t.Fatalf("identity contention did not yield without writes: %v", err)
			}
			args := tx.statements[0].args
			if args[5] != (*string)(nil) || *args[6].(*string) != healthautoexport.FallbackFingerprintVersion ||
				!bytes.Equal(args[7].([]byte), workout.FallbackSHA256[:]) {
				t.Fatal("fallback identity arguments changed")
			}
		})
	}
	permanent := &pgconn.PgError{Code: "23505"}
	if ingestCommitError(permanent) != permanent || ingestCommitError(nil) != nil {
		t.Fatal("nontransient errors changed classification")
	}
}

func TestIngestAdoptionOutboxFailureAbortsWorkout(t *testing.T) {
	document, err := healthautoexport.Parse(strings.NewReader(minimalExport))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("synthetic outbox failure")
	tx := &adoptionTx{outcome: "adopted", workoutID: uuid.New(), outboxErr: cause}
	_, err = (&Runner{}).persistWorkout(context.Background(), tx, claimedJob{}, uuid.New(), uuid.New(), document.Workouts[0], workouttimezone.Resolution{})
	if !errors.Is(err, cause) {
		t.Fatalf("outbox failure was ignored: %v", err)
	}
}

func TestIngestAdoptionManifestEncodingPreservesTransportIdentity(t *testing.T) {
	modified := time.Unix(1760000000, 123456789)
	for _, identity := range []string{"", "v2:rclone:" + strings.Repeat("a", 64)} {
		file := sourceFile{name: "HealthAutoExport-2026-01-02.json", exportDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			size: 42, observedModified: modified, device: 7, inode: 8, ctimeSec: 9, ctimeNS: 10, action: "process", observedIdentity: identity}
		encoded, err := encodeManifestCandidates([]sourceFile{file})
		if err != nil {
			t.Fatal(err)
		}
		var candidates []manifestCandidate
		if err := json.Unmarshal(encoded, &candidates); err != nil || len(candidates) != 1 {
			t.Fatalf("candidates=%s err=%v", encoded, err)
		}
		candidate := candidates[0]
		if candidate.Name != file.name || candidate.ObservedIdentity != identity || candidate.ModifiedSec != modified.Unix() ||
			candidate.ModifiedNS != int64(modified.Nanosecond()) || candidate.Device != file.device || candidate.Inode != file.inode || candidate.Action != "process" {
			t.Fatalf("manifest changed transport metadata: %+v", candidate)
		}
	}
	if encoded, err := encodeManifestCandidates(nil); err != nil || string(encoded) != "[]" {
		t.Fatalf("empty candidate set=%s err=%v", encoded, err)
	}
}
