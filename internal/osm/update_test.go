package osm

import (
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
	return []byte(`{"partitionPrepared":true,"preparedRegionId":"geofabrik:norcal","preparedGenerationId":42,"importerVersion":1,"derivationVersion":2,"provenanceMismatches":0,"sourceVersionMismatches":0,"logicalPathMismatches":0,"missingEndpointIndexes":0,"invalidWays":0,"invalidPathSegments":0,"orphanPathSegments":0,"materialLocalityResiduals":0,"ways":10,"pathSegments":20,"logicalPaths":5}`)
}

func newTestUpdater(t *testing.T, store *updateStoreStub, pipeline updatePipelineStub) Updater {
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
	if DerivationVersion != 2 {
		t.Fatalf("DerivationVersion=%d, want 2 for provider-region outside logical paths", DerivationVersion)
	}
	store := &updateStoreStub{}
	pipeline := updatePipelineStub{events: &store.events, validation: validUpdateReport()}
	updater := newTestUpdater(t, store, pipeline)
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err != nil {
		t.Fatal(err)
	}
	want := []string{"versions", "gc", "reserve", "download", "fileinfo", "record-download", "tags-filter", "check-refs", "osm2pgsql", "postprocess", "derive", "clip", "prepare-partitions", "validate", "set-validating", "promote", "gc"}
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
