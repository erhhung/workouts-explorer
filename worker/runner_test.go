package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestJobLifecycleJSONLogging(t *testing.T) {
	job := claimedJob{
		id: uuid.New(), kind: "manual_ingest_source", owner: "ada-owner",
		sourceName: "Morning Export", sourceType: "health-auto-export-local",
	}
	executionFailure := errors.New("database unavailable")
	results := ingestResults{FilesProcessed: 3, WorkoutsProcessed: 5, WorkoutsIngested: 4, FailedProcessing: []string{"HealthAutoExport-2026-01-01.json"}}
	for _, test := range []struct {
		name        string
		outcome     jobOutcome
		outcomeErr  error
		execute     func(context.Context, claimedJob) (executionResult, error)
		terminalMsg string
		terminalLvl string
		wantStatus  bool
		wantError   string
	}{
		{name: "completed", outcome: jobOutcome{status: "succeeded"}, terminalMsg: "job processing completed", terminalLvl: "INFO", wantStatus: true},
		{name: "partially completed", outcome: jobOutcome{status: "partially_succeeded"}, terminalMsg: "job processing completed", terminalLvl: "INFO", wantStatus: true},
		{name: "connection failure persisted as success", outcome: jobOutcome{status: "succeeded"}, terminalMsg: "job processing completed", terminalLvl: "INFO", wantStatus: true},
		{name: "failed", outcome: jobOutcome{status: "failed", failureCode: stringPointer("source-unavailable")}, terminalMsg: "job processing failed", terminalLvl: "ERROR", wantStatus: true, wantError: "job failed: source-unavailable"},
		{name: "cancelled", outcome: jobOutcome{status: "cancelled"}, terminalMsg: "job processing cancelled", terminalLvl: "INFO", wantStatus: true},
		{name: "running", outcome: jobOutcome{status: "running"}, terminalMsg: "job processing outcome unavailable", terminalLvl: "WARN", wantStatus: true},
		{name: "queued", outcome: jobOutcome{status: "queued"}, terminalMsg: "job processing outcome unavailable", terminalLvl: "WARN", wantStatus: true},
		{name: "no row", outcomeErr: pgx.ErrNoRows, terminalMsg: "job processing outcome unavailable", terminalLvl: "WARN", wantError: "no rows in result set"},
		{name: "query error", outcomeErr: errors.New("outcome query failed"), terminalMsg: "job processing outcome unavailable", terminalLvl: "WARN", wantError: "outcome query failed"},
		{name: "execution error", outcome: jobOutcome{status: "failed", failureCode: stringPointer("worker-error")}, execute: func(context.Context, claimedJob) (executionResult, error) {
			return executionResult{ingest: &results}, executionFailure
		}, terminalMsg: "job processing failed", terminalLvl: "ERROR", wantStatus: true, wantError: "database unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			runner := &Runner{
				logger: slog.New(slog.NewJSONHandler(&output, nil)),
				outcomeReader: func(context.Context, claimedJob) (jobOutcome, error) {
					return test.outcome, test.outcomeErr
				},
			}
			execute := test.execute
			if execute == nil {
				execute = func(context.Context, claimedJob) (executionResult, error) {
					return executionResult{ingest: &results}, nil
				}
			}
			err := runner.executeLogged(context.Background(), job, execute)
			if test.execute == nil && err != nil {
				t.Fatalf("executeLogged error=%v", err)
			}
			if test.execute != nil && (!errors.Is(err, executionFailure) || err == executionFailure) {
				t.Fatalf("logged wrapper did not preserve errors.Is: %v", err)
			}
			runner.logCycleFailure(context.Background(), err)
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("log lines=%d, want 2: %s", len(lines), output.String())
			}
			started, terminal := decodeLogLine(t, lines[0]), decodeLogLine(t, lines[1])
			if started["msg"] != "job processing started" || started["level"] != "INFO" ||
				terminal["msg"] != test.terminalMsg || terminal["level"] != test.terminalLvl {
				t.Fatalf("unexpected lifecycle logs: started=%v terminal=%v", started, terminal)
			}
			for _, record := range []map[string]any{started, terminal} {
				if record["job_id"] != job.id.String() || record["job_type"] != job.kind ||
					record["owner"] != job.owner ||
					record["source_name"] != job.sourceName || record["source_type"] != job.sourceType {
					t.Fatalf("missing structured context: %v", record)
				}
				if _, ok := record["owner_name"]; ok {
					t.Fatalf("record contains owner_name: %v", record)
				}
				if _, ok := record["source"]; ok {
					t.Fatalf("record contains combined source: %v", record)
				}
			}
			_, startedHasResults := started["results"]
			terminalResults, terminalHasResults := terminal["results"]
			wantResults := test.outcome.status == "succeeded" || test.outcome.status == "partially_succeeded" ||
				test.outcome.status == "failed" || test.outcome.status == "cancelled"
			if startedHasResults || terminalHasResults != wantResults {
				t.Fatalf("results presence started=%t terminal=%t, want terminal=%t", startedHasResults, terminalHasResults, wantResults)
			}
			if terminalHasResults {
				resultObject, ok := terminalResults.(map[string]any)
				if !ok || len(resultObject) != 4 || resultObject["files_processed"] != float64(3) ||
					resultObject["workouts_processed"] != float64(5) || resultObject["workouts_ingested"] != float64(4) {
					t.Fatalf("results=%v", terminalResults)
				}
			}
			if _, ok := started["duration_ms"]; ok {
				t.Fatal("start log contains terminal duration")
			}
			if duration, ok := terminal["duration_ms"].(float64); !ok || duration < 0 {
				t.Fatalf("invalid duration_ms=%v", terminal["duration_ms"])
			}
			if test.wantStatus && terminal["job_status"] != test.outcome.status {
				t.Fatalf("terminal status=%v, want %q", terminal["job_status"], test.outcome.status)
			}
			if test.wantError != "" && terminal["error"] != test.wantError {
				t.Fatalf("terminal error=%v, want %q", terminal["error"], test.wantError)
			}
			for _, secret := range []string{"/private/source/path", "config_envelope", "lease_token"} {
				if strings.Contains(output.String(), secret) {
					t.Fatalf("logs contain secret %q", secret)
				}
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func TestJobLogAttributesOmitSourceForNonSourceJob(t *testing.T) {
	attributes := jobLogAttributes(claimedJob{id: uuid.New(), kind: "workout_deletion", owner: "owner"})
	for index := 0; index < len(attributes); index += 2 {
		if attributes[index] == "source" || attributes[index] == "source_name" || attributes[index] == "source_type" {
			t.Fatalf("non-source attributes contain %q", attributes[index])
		}
	}
}

func TestConnectionCheckTerminalLogOmitsResults(t *testing.T) {
	var output bytes.Buffer
	runner := &Runner{
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
		outcomeReader: func(context.Context, claimedJob) (jobOutcome, error) {
			return jobOutcome{status: "succeeded"}, nil
		},
	}
	job := claimedJob{id: uuid.New(), kind: "source_connection_check", owner: "owner", sourceName: "source", sourceType: "local"}
	if err := runner.executeLogged(context.Background(), job, func(context.Context, claimedJob) (executionResult, error) {
		return executionResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if record := decodeLogLine(t, line); record["results"] != nil {
			t.Fatalf("source check log contains results: %v", record)
		}
	}
}

func TestCycleFailureLogsUnloggedErrors(t *testing.T) {
	var output bytes.Buffer
	runner := &Runner{logger: slog.New(slog.NewJSONHandler(&output, nil))}
	runner.logCycleFailure(context.Background(), errors.New("claim failed"))
	record := decodeLogLine(t, strings.TrimSpace(output.String()))
	if record["msg"] != "worker cycle failed" || record["error"] != "claim failed" {
		t.Fatalf("cycle failure log=%v", record)
	}
}

func decodeLogLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestCheckLocalConnection(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "export")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	keys := testKeyring(t)
	accountID, jobID := uuid.New(), uuid.New()

	t.Run("success", func(t *testing.T) {
		envelope := encryptSnapshot(t, keys, accountID, jobID, fmt.Sprintf(`{"version":1,"path":%q}`, directory))
		result := checkLocalConnection(context.Background(), keys, []string{root}, accountID, jobID, envelope)
		if result.status != "connected" || result.code != nil || result.summary != nil {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("inaccessible", func(t *testing.T) {
		path := filepath.Join(root, "missing", "private-value")
		envelope := encryptSnapshot(t, keys, accountID, jobID, fmt.Sprintf(`{"version":1,"path":%q}`, path))
		result := checkLocalConnection(context.Background(), keys, []string{root}, accountID, jobID, envelope)
		assertSafeFailure(t, result, "source-unavailable", path, string(envelope))
	})

	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "linked-private-value")
		if err := os.Symlink(directory, link); err != nil {
			t.Fatal(err)
		}
		envelope := encryptSnapshot(t, keys, accountID, jobID, fmt.Sprintf(`{"version":1,"path":%q}`, link))
		result := checkLocalConnection(context.Background(), keys, []string{root}, accountID, jobID, envelope)
		assertSafeFailure(t, result, "source-unavailable", link, string(envelope))
	})

	t.Run("strict decode", func(t *testing.T) {
		plaintext := fmt.Sprintf(`{"version":1,"path":%q,"secret":"private-value"}`, directory)
		envelope := encryptSnapshot(t, keys, accountID, jobID, plaintext)
		result := checkLocalConnection(context.Background(), keys, []string{root}, accountID, jobID, envelope)
		assertSafeFailure(t, result, "source-config-invalid", directory, "private-value", string(envelope))
	})

	t.Run("snapshot AAD", func(t *testing.T) {
		envelope := encryptSnapshot(t, keys, accountID, uuid.New(), fmt.Sprintf(`{"version":1,"path":%q}`, directory))
		result := checkLocalConnection(context.Background(), keys, []string{root}, accountID, jobID, envelope)
		assertSafeFailure(t, result, "source-config-invalid", directory, string(envelope))
	})

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result := checkLocalConnection(ctx, keys, []string{root}, accountID, jobID, []byte("private-envelope"))
		assertSafeFailure(t, result, "connection-check-cancelled", directory, "private-envelope")
	})
}

func TestHeartbeatFailureCancelsOperationBeforeCompletion(t *testing.T) {
	for _, heartbeatErr := range []error{errLeaseLost, errors.New("heartbeat failed")} {
		opCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		runHeartbeatOperation(cancel, done, func() error { return heartbeatErr })
		if opCtx.Err() == nil {
			t.Fatalf("operation was not cancelled for %v", heartbeatErr)
		}
		if err := <-done; !errors.Is(err, heartbeatErr) {
			t.Fatalf("heartbeat result=%v, want %v", err, heartbeatErr)
		}
	}
}

func assertSafeFailure(t *testing.T, result connectionResult, code string, sensitive ...string) {
	t.Helper()
	if result.status != "connection-failed" || result.code == nil || *result.code != code || result.summary == nil {
		t.Fatalf("result=%+v", result)
	}
	combined := *result.code + " " + *result.summary
	if len(*result.code) > 64 || len(*result.summary) > 512 {
		t.Fatal("failure metadata exceeds database bounds")
	}
	for _, value := range sensitive {
		if value != "" && strings.Contains(combined, value) {
			t.Fatalf("failure metadata contains sensitive value %q", value)
		}
	}
}

func testKeyring(t *testing.T) *sourcecrypto.Keyring {
	t.Helper()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	document := fmt.Sprintf(`{"activeKeyId":"test-key","keys":{"test-key":%q}}`, base64.RawURLEncoding.EncodeToString(key))
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := sourcecrypto.LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func encryptSnapshot(t *testing.T, keys *sourcecrypto.Keyring, accountID, jobID uuid.UUID, plaintext string) []byte {
	t.Helper()
	envelope, err := keys.Encrypt(sourcecrypto.Context{
		Purpose: sourcecrypto.JobConfigSnapshot, AccountID: accountID, RecordID: jobID,
	}, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
