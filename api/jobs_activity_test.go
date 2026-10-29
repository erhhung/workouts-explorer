package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestDecodeIngestActivity(t *testing.T) {
	updated := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, phase := range []string{"resuming", "preparing", "discovering", "importing", "analyzing_imported_routes", "analyzing_existing_routes", "updating_time_zones", "cleanup"} {
		t.Run(phase, func(t *testing.T) {
			activity, err := decodeIngestActivity("ingest-phase-"+strings.ReplaceAll(phase, "_", "-"), []byte(`{"attempt":2,"current":0,"total":9223372036854775807}`), updated)
			if err != nil || string(activity.Phase) != phase || activity.Current == nil || *activity.Current != 0 ||
				activity.Total == nil || *activity.Total != 9223372036854775807 || activity.UpdatedAt != updated {
				t.Fatalf("activity=%+v err=%v", activity, err)
			}
		})
	}
	for _, fields := range []string{`{}`, `{"attempt":2}`, `{"current":null,"total":null}`, `{"attempt":2,"private":"not exposed"}`} {
		activity, err := decodeIngestActivity("ingest-phase-preparing", []byte(fields), updated)
		if err != nil || activity.Current != nil || activity.Total != nil {
			t.Fatalf("unknown counters fields=%s activity=%+v err=%v", fields, activity, err)
		}
		encoded, err := json.Marshal(activity)
		if err != nil || string(encoded) != `{"phase":"preparing","updatedAt":"2026-10-28T12:00:00Z"}` {
			t.Fatalf("unknown counters must be omitted: %s err=%v", encoded, err)
		}
	}
	for _, fields := range []string{`{"current":2}`, `{"total":3}`} {
		if _, err := decodeIngestActivity("ingest-phase-importing", []byte(fields), updated); err != nil {
			t.Fatalf("independent optional counts %s: %v", fields, err)
		}
	}
	for _, code := range []string{"", "ingest-phase-complete", "ingest-phase-analyzing_imported_routes", "preparing", "ingest-phase-updating-timezones"} {
		if _, err := decodeIngestActivity(code, []byte(`{}`), updated); !errors.Is(err, errInvalidIngestActivity) {
			t.Fatalf("code=%q err=%v", code, err)
		}
	}
	for _, fields := range []string{"", `null`, " \nnull ", `[]`, `true`, `"private"`, `{`, `{"current":-1}`, `{"total":-1}`,
		`{"current":2,"total":1}`, `{"current":"1"}`, `{"total":1.5}`, `{"current":9223372036854775808}`, `{} {}`} {
		if _, err := decodeIngestActivity("ingest-phase-importing", []byte(fields), updated); !errors.Is(err, errInvalidIngestActivity) {
			t.Fatalf("invalid fields=%q err=%v", fields, err)
		}
	}
}

type ingestActivityTx struct {
	pgx.Tx
	query func(string, ...any) (pgx.Rows, error)
}

func (tx ingestActivityTx) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	return tx.query(query, args...)
}

func TestReadIngestActivitiesBatchedSourceDetails(t *testing.T) {
	root, first, legacy := uuid.New(), uuid.New(), uuid.New()
	detail := generated.JobDetail{Id: compactUUID(root), Children: []generated.JobDetail{
		{Id: compactUUID(first), Source: &generated.JobSourceContext{}},
		{Id: compactUUID(legacy), Source: &generated.JobSourceContext{}},
		{Id: compactUUID(uuid.New())},
	}}
	updated := time.Now().UTC()
	queries := 0
	tx := ingestActivityTx{query: func(query string, args ...any) (pgx.Rows, error) {
		queries++
		if len(args) != 1 || !reflect.DeepEqual(args[0], []uuid.UUID{first, legacy}) {
			t.Fatalf("activity query IDs=%v", args)
		}
		for _, fragment := range []string{"unnest($1::uuid[])", "CROSS JOIN LATERAL", "e.job_id=j.id", "e.account_id=j.account_id",
			"j.account_id=app.current_account_id()", "e.code LIKE 'ingest-phase-%'", "e.fields->>'attempt'=j.attempt::text",
			"ORDER BY e.id DESC LIMIT 1", "j.kind IN ('manual_ingest_source','scheduled_ingest_source')"} {
			if !strings.Contains(query, fragment) {
				t.Errorf("activity query missing %q", fragment)
			}
		}
		return &activityDisplayRows{rows: []activityDisplayRow{{first, "ingest-phase-importing", []byte(`{"attempt":2,"current":0}`), updated}}}, nil
	}}
	if err := readIngestActivities(context.Background(), tx, &detail); err != nil || queries != 1 {
		t.Fatalf("queries=%d err=%v", queries, err)
	}
	if detail.Activity != nil || detail.Children[1].Activity != nil || detail.Children[2].Activity != nil {
		t.Fatal("activity must not be invented for roots, legacy sources or non-ingest details")
	}
	activity := detail.Children[0].Activity
	if activity == nil || activity.Phase != generated.Importing || activity.Current == nil || *activity.Current != 0 || activity.Total != nil || activity.UpdatedAt != updated {
		t.Fatalf("child activity=%+v", activity)
	}
	standalone := detail.Children[0]
	standalone.Status = generated.JobStatus("succeeded")
	queries = 0
	tx.query = func(_ string, args ...any) (pgx.Rows, error) {
		queries++
		if !reflect.DeepEqual(args[0], []uuid.UUID{first}) {
			t.Fatalf("standalone IDs=%v", args)
		}
		return &activityDisplayRows{rows: []activityDisplayRow{{first, "ingest-phase-cleanup", []byte(`{}`), updated}}}, nil
	}
	if err := readIngestActivities(context.Background(), tx, &standalone); err != nil || queries != 1 ||
		standalone.Status != generated.JobStatus("succeeded") || standalone.Activity.Phase != generated.Cleanup {
		t.Fatalf("historical terminal activity=%+v queries=%d err=%v", standalone, queries, err)
	}
	for _, detail := range []generated.JobDetail{{}, {Children: []generated.JobDetail{{}}}} {
		if err := readIngestActivities(context.Background(), ingestActivityTx{}, &detail); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadIngestActivitiesErrors(t *testing.T) {
	id := uuid.New()
	detail := generated.JobDetail{Id: compactUUID(id), Source: &generated.JobSourceContext{}}
	queryErr := errors.New("query failed")
	for _, test := range []struct {
		name string
		rows []activityDisplayRow
		err  error
		want error
	}{
		{"query", nil, queryErr, queryErr},
		{"unknown phase", []activityDisplayRow{{id, "ingest-phase-invalid", []byte(`{}`), time.Now()}}, nil, errInvalidIngestActivity},
		{"invalid counters", []activityDisplayRow{{id, "ingest-phase-importing", []byte(`{"current":-1}`), time.Now()}}, nil, errInvalidIngestActivity},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := ingestActivityTx{query: func(string, ...any) (pgx.Rows, error) {
				return &activityDisplayRows{rows: test.rows}, test.err
			}}
			if err := readIngestActivities(context.Background(), tx, &detail); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
	detail.Id = "invalid"
	if err := readIngestActivities(context.Background(), ingestActivityTx{}, &detail); !errors.Is(err, errInvalidIngestActivity) {
		t.Fatalf("invalid stored source ID err=%v", err)
	}
}

func TestIngestActivityContractSerialization(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(openAPIDocument)
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["IngestActivity"].Value
	if schema.AdditionalProperties.Has == nil || *schema.AdditionalProperties.Has || !slices.Equal(schema.Required, []string{"phase", "updatedAt"}) {
		t.Fatal("activity must be closed with only phase and updatedAt required")
	}
	if len(schema.Properties) != 4 || !slices.Equal(schema.Properties["phase"].Value.Enum, []any{
		"resuming", "preparing", "discovering", "importing", "analyzing_imported_routes", "analyzing_existing_routes", "updating_time_zones", "cleanup",
	}) {
		t.Fatal("activity must expose exactly the approved phase enum and four fields")
	}
	for _, name := range []string{"current", "total"} {
		property := schema.Properties[name].Value
		if !property.Type.Includes("integer") || property.Format != "int64" || property.Min == nil || *property.Min != 0 || property.Nullable {
			t.Fatalf("%s must be optional non-null nonnegative int64", name)
		}
	}
	jobSchema := document.Components.Schemas["JobDetail"].Value
	if jobSchema.Properties["activity"].Value != schema || slices.Contains(jobSchema.Required, "activity") {
		t.Fatal("job detail must have optional typed activity")
	}
	for _, name := range []string{"JobSummary", "JobProgress", "JobFile"} {
		if document.Components.Schemas[name].Value.Properties["activity"] != nil || document.Components.Schemas[name].Value.Properties["phase"] != nil {
			t.Fatalf("%s must not carry phase/activity", name)
		}
	}
	for _, fields := range []string{`{}`, `{"current":0,"total":0}`, `{"current":null,"total":null}`} {
		activity, err := decodeIngestActivity("ingest-phase-cleanup", []byte(fields), time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		row := make(activityDisplayRow, 60)
		row[0], row[2], row[3] = uuid.New(), "manual_ingest_source", "succeeded"
		detail, _, err := scanJobDetail(row)
		if err != nil {
			t.Fatal(err)
		}
		detail.Activity = activity
		response := httptest.NewRecorder()
		writeJSON(response, http.StatusOK, detail)
		validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+detail.Id, response)
		encoded := response.Body.String()
		if strings.Contains(encoded, `"activity":null`) || !strings.Contains(encoded, `"phase":"cleanup"`) || strings.Contains(encoded, `"complete"`) {
			t.Fatalf("unexpected activity serialization: %s", encoded)
		}
		detail.Activity = nil
		encodedAbsent, err := json.Marshal(detail)
		if err != nil || strings.Contains(string(encodedAbsent), `"activity"`) {
			t.Fatalf("absent activity must be omitted: %s err=%v", encodedAbsent, err)
		}
	}
	for _, value := range []string{
		`{"phase":"complete","updatedAt":"2026-10-28T12:00:00Z"}`,
		`{"phase":"cleanup","updatedAt":"2026-10-28T12:00:00Z","current":-1}`,
		`{"phase":"cleanup","updatedAt":"2026-10-28T12:00:00Z","total":null}`,
		`{"phase":"cleanup","updatedAt":"2026-10-28T12:00:00Z","message":"private"}`,
		`{"phase":"cleanup"}`,
	} {
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			t.Fatal(err)
		}
		if err := schema.VisitJSON(decoded); err == nil {
			t.Fatalf("activity schema accepted %s", value)
		}
	}
}
