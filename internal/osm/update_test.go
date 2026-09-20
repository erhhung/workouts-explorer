package osm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type updateStoreStub struct {
	events      []string
	failures    []string
	gcCalls     int
	gcFailureAt int
	promoted    bool
}

func (s *updateStoreStub) Reserve(context.Context, string, ToolVersions) (Generation, error) {
	s.events = append(s.events, "reserve")
	return Generation{ID: 42, RegionID: "geofabrik:norcal", SchemaName: "osm_build_42", SourceURL: "https://download.geofabrik.de/north-america/us/california/norcal-latest.osm.pbf"}, nil
}
func (s *updateStoreStub) RecordDownload(context.Context, int64, DownloadResult, time.Time) error {
	s.events = append(s.events, "record-download")
	return nil
}
func (s *updateStoreStub) SetValidating(context.Context, int64, json.RawMessage) error {
	s.events = append(s.events, "set-validating")
	return nil
}
func (s *updateStoreStub) Promote(context.Context, Generation, json.RawMessage) error {
	s.events = append(s.events, "promote")
	s.promoted = true
	return nil
}
func (s *updateStoreStub) Fail(_ context.Context, _ Generation, summary string) error {
	s.events = append(s.events, "fail")
	s.failures = append(s.failures, summary)
	return nil
}
func (s *updateStoreStub) DropBuildSchema(context.Context, string) error {
	s.events = append(s.events, "drop-schema")
	return nil
}
func (s *updateStoreStub) ProcessStorageGC(context.Context) error {
	s.gcCalls++
	s.events = append(s.events, "gc")
	if s.gcFailureAt == s.gcCalls {
		return errors.New("GC unavailable")
	}
	return nil
}

type updatePipelineStub struct {
	events     *[]string
	failStage  string
	validation []byte
}

func (p updatePipelineStub) Versions(context.Context) (ToolVersions, error) {
	*p.events = append(*p.events, "versions")
	return ToolVersions{Osmium: ExpectedOsmium, Osm2pgsql: ExpectedOsm2pgsql}, nil
}
func (p updatePipelineStub) SourceTimestamp(context.Context, string) (time.Time, error) {
	*p.events = append(*p.events, "fileinfo")
	return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), nil
}
func (p updatePipelineStub) Run(_ context.Context, stage string, _ Generation, _, _ string) ([]byte, error) {
	*p.events = append(*p.events, stage)
	if stage == p.failStage {
		return nil, errors.New("stage failed with\nunsafe control")
	}
	if stage == "validate" {
		return p.validation, nil
	}
	return nil, nil
}

func validUpdateReport() []byte {
	return []byte(`{"partitionPrepared":true,"preparedRegionId":"geofabrik:norcal","preparedGenerationId":42,"importerVersion":3,"derivationVersion":16,"provenanceMismatches":0,"sourceVersionMismatches":0,"logicalPathMismatches":0,"missingEndpointIndexes":0,"invalidWays":0,"invalidPathSegments":0,"orphanPathSegments":0,"materialLocalityResiduals":0,"invalidParkAttributions":0,"invalidNationalParkAreas":0,"invalidEducationAttributions":0,"materialEducationResiduals":0,"remainingConnectedAttributionSplits":0,"attributionIdentityLogicalIdEdges":3,"attributionIdentityAffectedLogicalIds":5,"attributionIdentityMergedComponents":2,"attributionScopeRebasedSegments":0,"attributionScopeRebasedLogicalIds":0,"localityScopeSliversAbsorbed":0,"localityScopeSliverLengthMeters":0,"materialParkResiduals":0,"ways":10,"pathSegments":20,"logicalPaths":5}`)
}

func newTestUpdater(t *testing.T, store *updateStoreStub, pipeline UpdatePipeline) Updater {
	t.Helper()
	return Updater{
		Store: store, Pipeline: pipeline, HTTPClient: http.DefaultClient,
		Download: func(_ context.Context, _ *http.Client, _ Region, destination string, _ int64) (DownloadResult, error) {
			store.events = append(store.events, "download")
			if err := os.WriteFile(destination, []byte("pbf"), 0600); err != nil {
				return DownloadResult{}, err
			}
			return DownloadResult{Bytes: 3, SHA256: strings.Repeat("a", 64)}, nil
		},
		MaximumBytes: 100, ScratchRoot: t.TempDir(),
	}
}

func TestUpdaterRunsStagesInOrder(t *testing.T) {
	if ImporterVersion != 3 || DerivationVersion != 16 {
		t.Fatalf("importer/derivation versions=%d/%d, want 3/16 for educational-ground attribution", ImporterVersion, DerivationVersion)
	}
	store := &updateStoreStub{}
	pipeline := updatePipelineStub{events: &store.events, validation: validUpdateReport()}
	updater := newTestUpdater(t, store, pipeline)
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err != nil {
		t.Fatal(err)
	}
	want := []string{"versions", "gc", "reserve", "download", "fileinfo", "record-download", "tags-filter", "check-refs", "osm2pgsql", "postprocess", "derive", "clip", "attribute-parks", "prepare-partitions", "validate", "set-validating", "promote", "gc"}
	if !reflect.DeepEqual(store.events, want) {
		t.Fatalf("events = %v, want %v", store.events, want)
	}
}

func TestUpdaterFailureFailsGenerationAndCleansCandidate(t *testing.T) {
	store := &updateStoreStub{}
	pipeline := updatePipelineStub{events: &store.events, failStage: "derive", validation: validUpdateReport()}
	updater := newTestUpdater(t, store, pipeline)
	scratchRoot := updater.ScratchRoot
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil {
		t.Fatal("expected update failure")
	}
	if store.promoted || !reflect.DeepEqual(store.events[len(store.events)-2:], []string{"fail", "drop-schema"}) {
		t.Fatalf("incorrect pre-promotion cleanup: %v", store.events)
	}
	if len(store.failures) != 1 || strings.Contains(store.failures[0], "\n") || len(store.failures[0]) > 512 {
		t.Fatalf("unsafe failure summary %q", store.failures)
	}
	entries, err := os.ReadDir(scratchRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch was not removed: %v, %v", entries, err)
	}
}

func TestUpdaterValidationGatePreventsPromotion(t *testing.T) {
	store := &updateStoreStub{}
	report := strings.Replace(string(validUpdateReport()), `"invalidWays":0`, `"invalidWays":1`, 1)
	updater := newTestUpdater(t, store, updatePipelineStub{events: &store.events, validation: []byte(report)})
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil || !strings.Contains(err.Error(), "validation gate") {
		t.Fatalf("expected validation gate error, got %v", err)
	}
	if store.promoted {
		t.Fatal("invalid candidate was promoted")
	}
}

func TestUpdaterValidationRejectsMissingEndpointIndexes(t *testing.T) {
	store := &updateStoreStub{}
	report := strings.Replace(string(validUpdateReport()), `"missingEndpointIndexes":0`, `"missingEndpointIndexes":1`, 1)
	updater := newTestUpdater(t, store, updatePipelineStub{events: &store.events, validation: []byte(report)})
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil || !strings.Contains(err.Error(), "missingEndpointIndexes") {
		t.Fatalf("expected endpoint index validation error, got %v", err)
	}
	if store.promoted {
		t.Fatal("candidate without endpoint indexes was promoted")
	}
}

func TestUpdaterValidationRejectsInvalidNationalParkAreas(t *testing.T) {
	store := &updateStoreStub{}
	report := strings.Replace(string(validUpdateReport()), `"invalidNationalParkAreas":0`, `"invalidNationalParkAreas":1`, 1)
	updater := newTestUpdater(t, store, updatePipelineStub{events: &store.events, validation: []byte(report)})
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil || !strings.Contains(err.Error(), "invalidNationalParkAreas") {
		t.Fatalf("expected national park validation error, got %v", err)
	}
	if store.promoted {
		t.Fatal("candidate with an invalid national park was promoted")
	}
}

func TestUpdaterValidationRejectsConnectedAttributionSplit(t *testing.T) {
	store := &updateStoreStub{}
	report := strings.Replace(string(validUpdateReport()), `"remainingConnectedAttributionSplits":0`, `"remainingConnectedAttributionSplits":1`, 1)
	updater := newTestUpdater(t, store, updatePipelineStub{events: &store.events, validation: []byte(report)})
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil || !strings.Contains(err.Error(), "remainingConnectedAttributionSplits") {
		t.Fatalf("expected connected attribution split validation error, got %v", err)
	}
	if store.promoted {
		t.Fatal("candidate with a connected attribution split was promoted")
	}
}

func TestUpdaterPostPromotionGCFailureDoesNotFailActiveGeneration(t *testing.T) {
	store := &updateStoreStub{gcFailureAt: 2}
	updater := newTestUpdater(t, store, updatePipelineStub{events: &store.events, validation: validUpdateReport()})
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil || !strings.Contains(err.Error(), "remains retryable") {
		t.Fatalf("expected post-promotion cleanup error, got %v", err)
	}
	if !store.promoted || len(store.failures) != 0 || contains(store.events, "drop-schema") {
		t.Fatalf("active generation was treated as failed: %v", store.events)
	}
}

func TestCommandPipelineReportsExactVersions(t *testing.T) {
	if got := reportedVersion("osmium version 1.19.0\nlibosmium 2.20", "osmium"); got != "1.19.0" {
		t.Fatalf("version = %q", got)
	}
	if got := reportedVersion("osmium version 1.19.0 custom", "osmium"); got != "unreported" {
		t.Fatalf("accepted non-exact version %q", got)
	}
	if got := reportedVersion("osm2pgsql version 2.3.1 (2.3.1)", "osm2pgsql"); got != "2.3.1" {
		t.Fatalf("osm2pgsql version = %q", got)
	}
	if got := reportedVersion("osmium version 1.19.0 (v1.19.0)", "osmium"); got != "1.19.0" {
		t.Fatalf("osmium image version = %q", got)
	}
}

func TestPostgresConnectionEnvironmentSeparatesSafeDatabaseArgument(t *testing.T) {
	environment, databaseName, err := postgresConnectionEnvironment("postgresql://matcher:p%40ss@database.example:5433/osm%2Ddata?sslmode=verify-full&sslcert=%2Ftls%2Ftls.crt&sslkey=%2Ftls%2Ftls.key&sslrootcert=%2Ftls%2Fca.crt")
	if err != nil {
		t.Fatal(err)
	}
	if databaseName != "osm-data" {
		t.Fatalf("databaseName=%q", databaseName)
	}
	values := make(map[string]string)
	for _, item := range environment {
		name, value, found := strings.Cut(item, "=")
		if !found {
			t.Fatalf("invalid environment item %q", item)
		}
		values[name] = value
		if strings.Contains(item, "postgresql://") {
			t.Fatalf("environment retained complete database URL: %q", item)
		}
	}
	for name, want := range map[string]string{
		"PGHOST": "database.example", "PGPORT": "5433", "PGDATABASE": "osm-data", "PGUSER": "matcher", "PGPASSWORD": "p@ss",
		"PGSSLMODE": "verify-full", "PGSSLCERT": "/tls/tls.crt", "PGSSLKEY": "/tls/tls.key", "PGSSLROOTCERT": "/tls/ca.crt",
	} {
		if values[name] != want {
			t.Errorf("%s=%q, want %q", name, values[name], want)
		}
	}
	for _, invalid := range []string{"", "http://database/osm", "postgresql:///osm", "postgresql://database/", "postgresql://database/a/b"} {
		if _, _, err := postgresConnectionEnvironment(invalid); err == nil {
			t.Errorf("invalid URL %q was accepted", invalid)
		}
	}
}

func TestSourceTimestampUsesOsmiumHeader(t *testing.T) {
	timestamp, err := parseSourceTimestamp([]byte(`{"header":{"option":{"osmosis_replication_timestamp":"2026-08-27T21:00:00Z"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := timestamp.Format(time.RFC3339); got != "2026-08-27T21:00:00Z" {
		t.Fatalf("timestamp = %q", got)
	}
}

func TestSafeFailureRedactsAndBoundsSummary(t *testing.T) {
	message := safeFailure(errors.New("connect postgresql://user:secret@database/db\n" + strings.Repeat("x", 600)))
	if strings.Contains(message, "secret") || strings.Contains(message, "\n") || len([]rune(message)) > 512 {
		t.Fatalf("unsafe summary %q", message)
	}
}

func TestCommandPipelineRunsMutatingSQLStagesInOneTransaction(t *testing.T) {
	var calls [][]string
	pipeline := CommandPipeline{
		DatabaseURL: "postgresql://user:secret@database/osm",
		Root:        "/pipeline",
		outputCommand: func(_ context.Context, _ []string, name string, args ...string) (string, error) {
			if name != "psql" {
				t.Fatalf("command = %q, want psql", name)
			}
			calls = append(calls, append([]string(nil), args...))
			return "", nil
		},
		retryDelays: []time.Duration{},
	}
	generation := Generation{ID: 42, RegionID: "geofabrik:norcal", SchemaName: "osm_build_42"}
	for _, stage := range []string{"postprocess", "derive", "clip", "attribute-parks", "prepare-partitions"} {
		if _, err := pipeline.Run(context.Background(), stage, generation, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pipeline.Run(context.Background(), "validate", generation, "", ""); err != nil {
		t.Fatal(err)
	}
	for index, args := range calls {
		hasSingleTransaction := contains(args, "--single-transaction")
		if index < 5 && !hasSingleTransaction {
			t.Errorf("mutating call %d omitted --single-transaction: %v", index, args)
		}
		if index == 5 && hasSingleTransaction {
			t.Errorf("validate unexpectedly uses --single-transaction: %v", args)
		}
	}
	if got := calls[3]; !contains(got, "--file") || !contains(got, "/pipeline/attribute-parks.sql") {
		t.Fatalf("attribute-parks args = %v", got)
	}
}

func TestTransientPostgresFailureClassification(t *testing.T) {
	transient := []string{
		"SSL error: unexpected EOF while reading",
		"connection to server was lost",
		"connection to server was closed",
		"server closed the connection unexpectedly",
		"read: connection reset by peer",
		"write failed: broken pipe",
		"timeout connecting to host",
		"net/http: TLS handshake timeout",
		"dial tcp: i/o timeout",
		"could not connect to server",
		"connect: connection refused",
		"canceling statement due to conflict with recovery",
		"serialization failure (SQLSTATE 40001)",
		"terminating connection due to administrator command",
	}
	for _, message := range transient {
		t.Run(message, func(t *testing.T) {
			if !isTransientPostgresFailure(errors.New(strings.ToUpper(message))) {
				t.Fatalf("did not classify %q", message)
			}
		})
	}
	for _, message := range []string{
		"ERROR: syntax error at or near SELECT",
		"ERROR: duplicate key value violates unique constraint",
		"validation did not return one JSON object",
		"statement timeout",
	} {
		if isTransientPostgresFailure(errors.New(message)) {
			t.Errorf("classified permanent failure %q as transient", message)
		}
	}
	if isTransientPostgresFailure(context.Canceled) || isTransientPostgresFailure(context.DeadlineExceeded) {
		t.Fatal("classified context cancellation as transient")
	}
}

func TestCommandPipelineRetriesOnlyFailedStageAndLogsSafely(t *testing.T) {
	store := &updateStoreStub{}
	var stages []string
	var log strings.Builder
	attributeAttempts := 0
	pipeline := CommandPipeline{
		DatabaseURL: "postgresql://user:secret@database/osm",
		Root:        "/pipeline",
		Log:         &log,
		outputCommand: func(_ context.Context, _ []string, name string, args ...string) (string, error) {
			switch name {
			case "osmium":
				if contains(args, "--version") {
					return "osmium version 1.19.0", nil
				}
				if contains(args, "fileinfo") {
					return `{"header":{"option":{"osmosis_replication_timestamp":"2026-08-27T21:00:00Z"}}}`, nil
				}
				stages = append(stages, args[0])
			case "osm2pgsql":
				if contains(args, "--version") {
					return "osm2pgsql version 2.3.1", nil
				}
				stages = append(stages, "osm2pgsql")
			case "psql":
				stage := sqlStage(args)
				stages = append(stages, stage)
				if stage == "attribute-parks" {
					attributeAttempts++
					if attributeAttempts == 1 {
						return "", &commandFailure{name: "psql", output: "connect postgresql://user:secret@database/osm: SSL error: unexpected EOF while reading", cause: errors.New("exit status 2")}
					}
				}
				if stage == "validate" {
					return string(validUpdateReport()), nil
				}
			}
			return "", nil
		},
		retryDelays: []time.Duration{0, 0, 0, 0},
	}
	updater := newTestUpdater(t, store, pipeline)
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err != nil {
		t.Fatal(err)
	}
	wantStages := []string{"tags-filter", "check-refs", "osm2pgsql", "postprocess", "derive", "clip", "attribute-parks", "attribute-parks", "prepare-partitions", "validate"}
	if !reflect.DeepEqual(stages, wantStages) {
		t.Fatalf("stage attempts = %v, want %v", stages, wantStages)
	}
	if len(store.failures) != 0 || contains(store.events, "drop-schema") {
		t.Fatalf("successful retry failed or dropped generation: %v", store.events)
	}
	if message := log.String(); !strings.Contains(message, "attribute-parks") || !strings.Contains(message, "attempt 1/5") || !strings.Contains(message, "retrying in 0s") || strings.Contains(message, "secret") {
		t.Fatalf("unsafe or incomplete retry log %q", message)
	}
}

func TestCommandPipelineStreamsOutputAndReportsLongRunningProgress(t *testing.T) {
	var log bytes.Buffer
	pipeline := CommandPipeline{Log: &log, progressInterval: 5 * time.Millisecond}
	output, err := pipeline.output(context.Background(), nil, "sh", "-c", "sleep 0.03; printf progress-output")
	if err != nil {
		t.Fatal(err)
	}
	if output != "progress-output" {
		t.Fatalf("output = %q", output)
	}
	message := log.String()
	if !strings.Contains(message, "running OSM tool sh") ||
		!strings.Contains(message, "OSM tool sh still running") ||
		!strings.Contains(message, "progress-output") {
		t.Fatalf("missing streamed progress in %q", message)
	}
}

func TestCommandPipelineDoesNotRetryPermanentFailureOrCancellation(t *testing.T) {
	generation := Generation{ID: 42, RegionID: "geofabrik:norcal", SchemaName: "osm_build_42"}
	for _, test := range []struct {
		name string
		ctx  func() context.Context
		err  error
	}{
		{name: "permanent", ctx: context.Background, err: errors.New("ERROR: syntax error at or near SELECT")},
		{name: "canceled", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, err: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			pipeline := CommandPipeline{DatabaseURL: "postgresql://database/osm", Root: "/pipeline", retryDelays: []time.Duration{0, 0, 0, 0}, outputCommand: func(context.Context, []string, string, ...string) (string, error) {
				calls++
				return "", test.err
			}}
			if _, err := pipeline.Run(test.ctx(), "attribute-parks", generation, "", ""); err == nil {
				t.Fatal("expected failure")
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1", calls)
			}
		})
	}
}

func TestCommandPipelineRetryExhaustionUsesUpdaterCleanup(t *testing.T) {
	store := &updateStoreStub{}
	attributeAttempts := 0
	stageCounts := make(map[string]int)
	pipeline := CommandPipeline{
		DatabaseURL: "postgresql://database/osm",
		Root:        "/pipeline",
		outputCommand: func(_ context.Context, _ []string, name string, args ...string) (string, error) {
			switch name {
			case "osmium":
				if contains(args, "--version") {
					return "osmium version 1.19.0", nil
				}
				if contains(args, "fileinfo") {
					return `{"header":{"option":{"timestamp":"2026-08-27T21:00:00Z"}}}`, nil
				}
			case "osm2pgsql":
				if contains(args, "--version") {
					return "osm2pgsql version 2.3.1", nil
				}
				stageCounts["osm2pgsql"]++
			case "psql":
				stage := sqlStage(args)
				stageCounts[stage]++
				if stage == "attribute-parks" {
					attributeAttempts++
					return "", errors.New("connection to server was lost")
				}
			}
			return "", nil
		},
		retryDelays: []time.Duration{0, 0, 0, 0},
	}
	updater := newTestUpdater(t, store, pipeline)
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil {
		t.Fatal("expected retries to be exhausted")
	}
	if attributeAttempts != 5 || stageCounts["derive"] != 1 || stageCounts["clip"] != 1 {
		t.Fatalf("unexpected stage counts: attributes=%d all=%v", attributeAttempts, stageCounts)
	}
	if !reflect.DeepEqual(store.events[len(store.events)-2:], []string{"fail", "drop-schema"}) {
		t.Fatalf("missing updater cleanup: %v", store.events)
	}
}

func sqlStage(args []string) string {
	for index, arg := range args {
		if arg == "--file" && index+1 < len(args) {
			stage := strings.TrimSuffix(filepath.Base(args[index+1]), ".sql")
			if stage == "derive-compact" {
				return "derive"
			}
			if stage == "clip-localities" {
				return "clip"
			}
			return stage
		}
	}
	return ""
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestUpdaterUsesGenerationScopedScratch(t *testing.T) {
	store := &updateStoreStub{}
	updater := newTestUpdater(t, store, updatePipelineStub{events: &store.events, validation: validUpdateReport()})
	updater.Download = func(_ context.Context, _ *http.Client, _ Region, destination string, _ int64) (DownloadResult, error) {
		if filepath.Base(filepath.Dir(destination)) != "generation-42" {
			t.Fatalf("destination = %s", destination)
		}
		return DownloadResult{}, errors.New("stop")
	}
	_, _ = updater.Run(context.Background(), "geofabrik:norcal")
}
