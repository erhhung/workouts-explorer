package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestReadSourceFileKeepsProviderActivityIdentity(t *testing.T) {
	for _, label := range []string{"Outdoor Running", "Outdoor Walking", "Outdoor Biking", "Indoor Running", "Indoor Walking", "Indoor Cycling", "Cycling", "Unknown private activity"} {
		t.Run(label, func(t *testing.T) {
			root := t.TempDir()
			raw := fmt.Sprintf(`{"data":{"workouts":[{"name":%q,"start":"2026-08-01 12:00:00 +0000","end":"2026-08-01 12:02:00 +0000","duration":120,"isIndoor":true}]}}`, label)
			if err := os.WriteFile(filepath.Join(root, "HealthAutoExport-2026-08-01.json"), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			directory, err := sourceconfig.OpenDirectory(root, []string{root})
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			files, err := discoverSourceFiles(directory)
			if err != nil || len(files) != 1 {
				t.Fatalf("files=%v err=%v", files, err)
			}
			parsed, failure := readSourceFile(context.Background(), directory, files[0])
			if failure != nil {
				t.Fatal(failure)
			}
			doc, err := healthautoexport.Parse(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			got, want := parsed.document.Workouts[0], doc.Workouts[0]
			_ = healthautoexport.CanonicalActivityLabel(got.ProviderLabel)
			if got.ProviderLabel != label || got.TypeKey != want.TypeKey || got.ContentSHA256 != want.ContentSHA256 || got.FallbackSHA256 != want.FallbackSHA256 {
				t.Fatal("ingest parsing changed raw provider identity")
			}
		})
	}
}

func TestCanonicalCycleDisplayPreservesPipelineMovementMode(t *testing.T) {
	for _, prefix := range []string{"Indoor", "Outdoor"} {
		for _, alias := range []string{"Cycle", "Cycling", "Bike", "Biking", "Bicycle", "Bicycling"} {
			label := prefix + " " + alias
			key := healthautoexport.NormalizedTypeKey(label)
			if raw, display := routepipeline.MovementMode(key, label), routepipeline.MovementMode(key, healthautoexport.CanonicalActivityLabel(label)); raw != coverage.MovementBicycle || display != raw {
				t.Fatalf("%q movement mode changed: raw=%s display=%s", label, raw, display)
			}
		}
	}
}

type activityIngestRow func(...any) error

func (row activityIngestRow) Scan(destinations ...any) error { return row(destinations...) }

type activityIngestTx struct {
	pgx.Tx
	prior        activityIngestRow
	typeID       uuid.UUID
	writes       map[string][]any
	derivedCalls int
	suppressed   bool
}

func (tx *activityIngestTx) QueryRow(_ context.Context, query string, arguments ...any) pgx.Row {
	switch {
	case strings.HasPrefix(query, "INSERT INTO app.workout_types"):
		tx.writes[query] = arguments
		return activityIngestRow(func(destinations ...any) error { *destinations[0].(*uuid.UUID) = tx.typeID; return nil })
	case strings.HasPrefix(query, "SELECT id,content_sha256"):
		return tx.prior
	case strings.Contains(query, "workout_deletion_suppressed"):
		tx.writes[query] = arguments
		return activityIngestRow(func(destinations ...any) error { *destinations[0].(*bool) = tx.suppressed; return nil })
	default:
		if strings.Contains(query, "replace_workout_") {
			tx.derivedCalls++
		}
		return activityIngestRow(func(destinations ...any) error {
			*destinations[0].(*bool) = true
			return nil
		})
	}
}

func TestPersistWorkoutSuppressionUsesRawActivityIdentity(t *testing.T) {
	for _, label := range []string{"Outdoor Running", "Outdoor Walking", "Outdoor Biking", "IndoorRunning", "IndoorWalking", "IndoorCycling"} {
		for _, providerID := range []string{"", "opaque/provider:v1", uuid.NewString()} {
			t.Run(label+"/"+providerID, func(t *testing.T) {
				raw := fmt.Sprintf(`{"data":{"workouts":[{"id":%q,"name":%q,"start":"2026-08-01 12:00:00 +0000","end":"2026-08-01 12:02:00 +0000","duration":120}]}}`, providerID, label)
				document, err := healthautoexport.Parse(strings.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				workout := document.Workouts[0]
				tx := &activityIngestTx{suppressed: true, writes: make(map[string][]any)}
				sourceID := uuid.New()
				kind, err := (&Runner{}).persistWorkout(context.Background(), tx, claimedJob{}, sourceID, uuid.New(), workout)
				if err != nil || kind != "suppressed" || len(tx.writes) != 1 || tx.derivedCalls != 0 {
					t.Fatalf("suppression=%s writes=%d derived=%d err=%v", kind, len(tx.writes), tx.derivedCalls, err)
				}
				for _, arguments := range tx.writes {
					id, version, hash := arguments[1].(*string), arguments[2].(*string), arguments[3].([]byte)
					if arguments[0] != sourceID {
						t.Fatal("suppression changed source scope")
					}
					if providerID == "" {
						if id != nil || version == nil || *version != healthautoexport.FallbackFingerprintVersion || !bytes.Equal(hash, workout.FallbackSHA256[:]) {
							t.Fatal("suppression replaced raw fallback v2 identity")
						}
					} else if id == nil || *id != providerID || version != nil || len(hash) != 0 {
						t.Fatal("suppression replaced opaque/UUID provider identity")
					}
				}
			})
		}
	}
}

func (tx *activityIngestTx) Exec(_ context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
	tx.writes[query] = arguments
	if strings.Contains(query, "workout_aggregates") || strings.Contains(query, "workout_route_points") {
		tx.derivedCalls++
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestPersistWorkoutCanonicalDisplayWithoutIdentityRewrite(t *testing.T) {
	for _, label := range []string{"Outdoor Running", "Outdoor Walking", "Outdoor Biking", "IndoorRunning", "IndoorWalking", "IndoorCycling", "Cycling", "Private Unknown"} {
		for _, kind := range []string{"created", "updated", "matched_unchanged"} {
			t.Run(label+"/"+kind, func(t *testing.T) {
				raw := fmt.Sprintf(`{"data":{"workouts":[{"name":%q,"start":"2026-08-01 12:00:00 +0000","end":"2026-08-01 12:02:00 +0000","duration":120}]}}`, label)
				document, err := healthautoexport.Parse(strings.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				workout := document.Workouts[0]
				original := workout
				workoutID, fileID, typeID := uuid.New(), uuid.New(), uuid.New()
				tx := &activityIngestTx{typeID: typeID, writes: make(map[string][]any)}
				tx.prior = func(destinations ...any) error {
					if kind == "created" {
						return pgx.ErrNoRows
					}
					*destinations[0].(*uuid.UUID) = workoutID
					*destinations[1].(*[]byte) = []byte{1}
					if kind == "matched_unchanged" {
						*destinations[1].(*[]byte) = workout.ContentSHA256[:]
					}
					return nil
				}
				runner := &Runner{}
				got, err := runner.persistWorkout(context.Background(), tx, claimedJob{accountID: uuid.New(), id: uuid.New()}, uuid.New(), fileID, workout)
				if err != nil || got != kind || !reflect.DeepEqual(workout, original) {
					t.Fatalf("kind=%s err=%v or raw workout changed", got, err)
				}
				canonical := healthautoexport.CanonicalActivityLabel(label)
				for query, arguments := range tx.writes {
					switch {
					case strings.HasPrefix(query, "INSERT INTO app.workout_types"):
						if arguments[2] != original.TypeKey || arguments[3] != canonical {
							t.Fatal("type key/display changed incorrectly")
						}
					case strings.HasPrefix(query, "INSERT INTO app.workouts"):
						if arguments[4] != typeID || !bytes.Equal(arguments[7].([]byte), original.FallbackSHA256[:]) || !bytes.Equal(arguments[8].([]byte), original.ContentSHA256[:]) || arguments[9] != canonical {
							t.Fatal("insert display replaced provider identity/hash")
						}
					case strings.HasPrefix(query, "UPDATE app.workouts SET source_file_id"):
						if arguments[0] != workoutID || arguments[2] != typeID || !bytes.Equal(arguments[3].([]byte), original.ContentSHA256[:]) || arguments[4] != canonical {
							t.Fatal("update display replaced provider identity/hash")
						}
					case strings.HasPrefix(query, "UPDATE app.workouts SET provider_label"):
						if arguments[0] != workoutID || arguments[1] != canonical || arguments[2] != fileID || strings.Contains(query, "content_sha256") {
							t.Fatal("display-only repair rewrote content identity")
						}
					case strings.HasPrefix(query, "INSERT INTO app.workout_import_events"):
						if arguments[6] != kind || !bytes.Equal(arguments[7].([]byte), original.ContentSHA256[:]) {
							t.Fatal("import event kind/hash changed")
						}
					}
				}
				if kind == "matched_unchanged" && tx.derivedCalls != 0 {
					t.Fatal("unchanged display repair touched metrics or route summaries")
				}
			})
		}
	}
}
