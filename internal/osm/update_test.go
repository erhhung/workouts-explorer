package osm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type maintainedUpdatePipelineStub struct {
	updatePipelineStub
	maintenanceCalls int
}

func (p *maintainedUpdatePipelineStub) Maintain(_ context.Context, stage string, _ Generation) error {
	if stage == "identity-propagate" {
		p.maintenanceCalls++
	}
	return nil
}

type fixedStoragePreflight struct{ observation StorageObservation }

func (p fixedStoragePreflight) Check(context.Context) (StorageObservation, error) {
	return p.observation, nil
}

type sequenceStoragePreflight struct {
	observations []StorageObservation
	calls        int
}

func (p *sequenceStoragePreflight) Check(context.Context) (StorageObservation, error) {
	observation := p.observations[p.calls]
	p.calls++
	return observation, nil
}

type resumableUpdateStoreStub struct {
	*updateStoreStub
	stageChecks   map[string]int
	completeAfter map[string]int
}

func (s *updateStoreStub) LockUpdater(context.Context) (func(), error) {
	return func() {}, nil
}

func (s resumableUpdateStoreStub) LockGeneration(context.Context, int64) (func(), error) {
	return func() {}, nil
}

func (s resumableUpdateStoreStub) Resume(context.Context, int64, string, ToolVersions) (Generation, map[string]bool, error) {
	s.events = append(s.events, "resume")
	return Generation{ID: 42, RegionID: "geofabrik:norcal", SchemaName: "osm_build_42", SourceURL: "unused"}, map[string]bool{
		"tags-filter": true, "check-refs": true, "osm2pgsql": true, "postprocess": true, "derive": true,
		"clip-candidates": true, "clip-replacements": true, "clip-apply": true, "clip-residual": true, "clip-finalize": true,
		"attribute-parks-tags": true, "attribute-education": true, "attribute-slivers": true,
		"identity-segments": true, "identity-edges": true, "identity-proximity": true,
		"identity-components": true, "identity-propagate": true, "identity-label-overrides": true,
	}, nil
}
func (s resumableUpdateStoreStub) StartStage(_ context.Context, _ int64, stage string, _ int, _ string) error {
	s.events = append(s.events, "start:"+stage)
	return nil
}
func (s resumableUpdateStoreStub) StageCompleted(_ context.Context, _ int64, stage string) (bool, error) {
	if s.stageChecks == nil {
		return true, nil
	}
	s.stageChecks[stage]++
	return s.stageChecks[stage] >= s.completeAfter[stage], nil
}
func (s resumableUpdateStoreStub) StageFenceMatches(context.Context, int64, string, string) (bool, error) {
	return true, nil
}
func (s resumableUpdateStoreStub) CompleteStage(_ context.Context, _ int64, stage string) error {
	s.events = append(s.events, "complete:"+stage)
	return nil
}
func (s resumableUpdateStoreStub) FailStage(_ context.Context, _ Generation, stage, _ string) error {
	s.events = append(s.events, "fail-stage:"+stage)
	return nil
}
func (s resumableUpdateStoreStub) RecordStorageBlock(_ context.Context, _ Generation, stage string, blocked *StoragePreflightError) error {
	s.events = append(s.events, "block:"+stage+":"+string(blocked.Code))
	return nil
}

type storagePreflightStub struct {
	events *[]string
	calls  int
	failAt int
}

type allowStoragePreflight struct{}

func (allowStoragePreflight) Check(context.Context) (StorageObservation, error) {
	return StorageObservation{}, nil
}

func (s *storagePreflightStub) Check(context.Context) (StorageObservation, error) {
	s.calls++
	*s.events = append(*s.events, "preflight")
	if s.calls == s.failAt {
		observation := StorageObservation{PVCName: "pgdata", CapacityBytes: 1000, FreeBytes: 99, RequiredFreeBytes: 100}
		return observation, &StoragePreflightError{Code: StorageInsufficientSpace, Observation: observation, Cause: errors.New("low storage")}
	}
	return StorageObservation{}, nil
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
	return []byte(`{"partitionPrepared":true,"preparedRegionId":"geofabrik:norcal","preparedGenerationId":42,"importerVersion":3,"derivationVersion":20,"provenanceMismatches":0,"sourceVersionMismatches":0,"logicalPathMismatches":0,"missingEndpointIndexes":0,"invalidWays":0,"invalidPathSegments":0,"orphanPathSegments":0,"materialLocalityResiduals":0,"invalidParkAttributions":0,"invalidNationalParkAreas":0,"invalidStateParkAreas":0,"invalidEducationAttributions":0,"materialEducationResiduals":0,"remainingConnectedAttributionSplits":0,"attributionIdentityLogicalIdEdges":3,"attributionIdentityAffectedLogicalIds":5,"attributionIdentityMergedComponents":2,"attributionScopeRebasedSegments":0,"attributionScopeRebasedLogicalIds":0,"localityScopeSliversAbsorbed":0,"localityScopeSliverLengthMeters":0,"materialParkResiduals":0,"ways":10,"pathSegments":20,"logicalPaths":5}`)
}

func TestUpdaterResumesAtFirstIncompleteDatabaseStageAndPreservesBuild(t *testing.T) {
	base := &updateStoreStub{}
	store := resumableUpdateStoreStub{updateStoreStub: base}
	pipeline := updatePipelineStub{events: &base.events, failStage: "identity-rewrite", validation: validUpdateReport()}
	updater := newTestUpdater(t, base, pipeline)
	updater.Store = store
	updater.ResumeGenerationID = 42
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil {
		t.Fatal("expected resumed stage failure")
	}
	joined := strings.Join(base.events, ",")
	for _, forbidden := range []string{"versions", "reserve", "download", "tags-filter", "check-refs", "osm2pgsql", "postprocess", "derive", "clip", "fail", "drop-schema"} {
		for _, event := range base.events {
			if event == forbidden {
				t.Fatalf("resume unexpectedly ran %q: %s", forbidden, joined)
			}
		}
	}
	for _, required := range []string{"resume", "start:identity-rewrite", "identity-rewrite", "fail-stage:identity-rewrite"} {
		found := false
		for _, event := range base.events {
			if event == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("resume missing %q: %s", required, joined)
		}
	}
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
		StoragePreflight: allowStoragePreflight{},
	}
}

func TestUpdaterRunsStagesInOrder(t *testing.T) {
	if ImporterVersion != 3 || DerivationVersion != 20 {
		t.Fatalf("importer/derivation versions=%d/%d, want 3/20 for direction-preserving named-road identities", ImporterVersion, DerivationVersion)
	}
	store := &updateStoreStub{}
	pipeline := updatePipelineStub{events: &store.events, validation: validUpdateReport()}
	updater := newTestUpdater(t, store, pipeline)
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err != nil {
		t.Fatal(err)
	}
	want := []string{"versions", "gc", "reserve", "download", "fileinfo", "record-download", "tags-filter", "check-refs", "osm2pgsql", "postprocess", "derive", "clip-candidates", "clip-replacements", "clip-apply", "clip-residual", "clip-finalize", "attribute-parks-tags", "attribute-education", "attribute-slivers", "identity-segments", "identity-edges", "identity-proximity", "identity-components", "identity-propagate", "identity-label-overrides", "identity-rewrite", "prepare-partitions", "validate", "set-validating", "promote", "gc"}
	if !reflect.DeepEqual(store.events, want) {
		t.Fatalf("events = %v, want %v", store.events, want)
	}
}

func TestUpdaterLogsLocalGenerationAndStageBanners(t *testing.T) {
	store := &updateStoreStub{}
	pipeline := updatePipelineStub{events: &store.events, validation: validUpdateReport()}
	updater := newTestUpdater(t, store, pipeline)
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	now := time.Date(2026, 9, 26, 6, 10, 20, 0, time.UTC)
	updater.Log, updater.LogLocation = &log, location
	updater.Now = func() time.Time {
		current := now
		now = now.Add(time.Second)
		return current
	}
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`2026-09-25 23:10:22 ====== Starting OSM update generation 42 (derivation v20) batch "download" ======`,
		`====== OSM update generation 42 (derivation v20) batch "download" completed in 2s ======`,
		`====== Starting OSM update generation 42 (derivation v20) batch "tags-filter" ======`,
		`====== OSM update generation 42 (derivation v20) batch "tags-filter" completed in 2s ======`,
		`====== OSM update generation 42 completed in`,
	} {
		if !strings.Contains(log.String(), expected) {
			t.Errorf("log does not contain %q:\n%s", expected, log.String())
		}
	}
	if !strings.Contains(log.String(), `batch "tags-filter" completed in 2s ======`+"\n\n") {
		t.Fatalf("completion banner lacks a trailing blank line:\n%s", log.String())
	}
}

func TestUpdaterLogsBatchStorageObservations(t *testing.T) {
	var log bytes.Buffer
	updater := Updater{Log: &log}
	updater.logStorageStart(StorageObservation{
		PVCName: "data-postgresql-postgresql-0", NodeName: "k8s4",
		CapacityBytes: 32145145856, FreeBytes: 7827577897,
	})
	updater.logStorageCompletion(
		StorageObservation{FreeBytes: 2555505541},
		StorageObservation{PVCName: "data-postgresql-postgresql-0", NodeName: "k8s4", CapacityBytes: 32145145856, FreeBytes: 7548407316},
	)
	want := "PVC: data-postgresql-postgresql-0 (k8s4) | capacity: 29.94 GiB | free: 7.29 GiB (24%)\n" +
		"PVC: data-postgresql-postgresql-0 (k8s4) | pre-GC: 2.38 GiB | post-GC: 7.03 GiB (23%)\n"
	if log.String() != want {
		t.Fatalf("storage log=%q, want %q", log.String(), want)
	}
}

func TestIdentityEvaluationUsesProductionIdentityStages(t *testing.T) {
	first := -1
	for index, stage := range pipelineStages {
		if stage == identityPipelineStages[0] {
			first = index
			break
		}
	}
	if first < 0 || first+len(identityPipelineStages) > len(pipelineStages) ||
		!reflect.DeepEqual(pipelineStages[first:first+len(identityPipelineStages)], identityPipelineStages) {
		t.Fatalf("identity evaluator stages %v are not the production stage sequence %v", identityPipelineStages, pipelineStages)
	}
}

func TestUpdaterPreflightsEveryCommittedBatch(t *testing.T) {
	base := &updateStoreStub{}
	store := resumableUpdateStoreStub{updateStoreStub: base, stageChecks: map[string]int{}, completeAfter: map[string]int{"clip-candidates": 3}}
	pipeline := updatePipelineStub{events: &base.events, validation: validUpdateReport()}
	preflight := &storagePreflightStub{events: &base.events}
	updater := newTestUpdater(t, base, pipeline)
	updater.Store = store
	updater.StoragePreflight = preflight
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err != nil {
		t.Fatal(err)
	}
	clipRuns := 0
	for index, event := range base.events {
		if event == "clip-candidates" {
			clipRuns++
			if index == 0 || base.events[index-1] != "preflight" {
				t.Fatalf("clip batch was not immediately preflighted: %v", base.events)
			}
		}
	}
	if clipRuns != 3 {
		t.Fatalf("clip candidate runs=%d, want 3", clipRuns)
	}
}

func TestUpdaterVacuumsPropagationOnlyBelowFifteenPercentFree(t *testing.T) {
	for _, test := range []struct {
		name      string
		freeBytes int64
		wantCalls int
	}{
		{name: "below threshold", freeBytes: 14, wantCalls: 1},
		{name: "at threshold", freeBytes: 15, wantCalls: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := &updateStoreStub{}
			store := resumableUpdateStoreStub{updateStoreStub: base}
			pipeline := &maintainedUpdatePipelineStub{updatePipelineStub: updatePipelineStub{events: &base.events, validation: validUpdateReport()}}
			updater := newTestUpdater(t, base, pipeline.updatePipelineStub)
			updater.Store, updater.Pipeline = store, pipeline
			updater.StoragePreflight = fixedStoragePreflight{observation: StorageObservation{CapacityBytes: 100, FreeBytes: test.freeBytes}}
			if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err != nil {
				t.Fatal(err)
			}
			if pipeline.maintenanceCalls != test.wantCalls {
				t.Fatalf("maintenance calls=%d, want %d", pipeline.maintenanceCalls, test.wantCalls)
			}
		})
	}
}

func TestStorageBelowPercentUsesStrictLowWaterThreshold(t *testing.T) {
	for _, test := range []struct {
		free int64
		want bool
	}{
		{free: 14, want: true},
		{free: 15, want: false},
		{free: 16, want: false},
	} {
		if got := storageBelowPercent(StorageObservation{CapacityBytes: 100, FreeBytes: test.free}, 15); got != test.want {
			t.Errorf("free=%d: storageBelowPercent=%v, want %v", test.free, got, test.want)
		}
	}
}

func TestPostGCStorageObservationWaitsOnceWhenFirstSampleIsBelowLowWater(t *testing.T) {
	finished := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	preflight := &sequenceStoragePreflight{observations: []StorageObservation{
		{SampledAt: finished.Add(time.Second), FreeBytes: 90},
		{SampledAt: finished.Add(31 * time.Second), FreeBytes: 120},
	}}
	updater := Updater{StoragePreflight: preflight}
	observation, err := updater.postGCStorageObservation(context.Background(), StorageObservation{FreeBytes: 100}, finished)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.calls != 2 || observation.FreeBytes != 120 {
		t.Fatalf("calls=%d observation=%+v", preflight.calls, observation)
	}
}

func TestPostGCStorageObservationKeepsFirstRecoveredSample(t *testing.T) {
	finished := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	preflight := &sequenceStoragePreflight{observations: []StorageObservation{
		{SampledAt: finished.Add(time.Second), FreeBytes: 100},
	}}
	updater := Updater{StoragePreflight: preflight}
	observation, err := updater.postGCStorageObservation(context.Background(), StorageObservation{FreeBytes: 100}, finished)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.calls != 1 || observation.FreeBytes != 100 {
		t.Fatalf("calls=%d observation=%+v", preflight.calls, observation)
	}
}

func TestUpdaterStorageBlockPreservesResumableGeneration(t *testing.T) {
	base := &updateStoreStub{}
	store := resumableUpdateStoreStub{updateStoreStub: base}
	pipeline := updatePipelineStub{events: &base.events, validation: validUpdateReport()}
	preflight := &storagePreflightStub{events: &base.events, failAt: 1}
	updater := newTestUpdater(t, base, pipeline)
	updater.Store, updater.ResumeGenerationID, updater.StoragePreflight = store, 42, preflight
	_, err := updater.Run(context.Background(), "geofabrik:norcal")
	if err == nil || !strings.Contains(err.Error(), "storage preflight") {
		t.Fatalf("expected storage block, got %v", err)
	}
	for _, forbidden := range []string{"identity-rewrite", "fail-stage:identity-rewrite", "fail", "drop-schema"} {
		if contains(base.events, forbidden) {
			t.Fatalf("storage block performed %q: %v", forbidden, base.events)
		}
	}
	if !contains(base.events, "block:identity-rewrite:insufficient_space") {
		t.Fatalf("storage block was not recorded: %v", base.events)
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

func TestUpdaterValidationRejectsInvalidStateParkAreas(t *testing.T) {
	store := &updateStoreStub{}
	report := strings.Replace(string(validUpdateReport()), `"invalidStateParkAreas":0`, `"invalidStateParkAreas":1`, 1)
	updater := newTestUpdater(t, store, updatePipelineStub{events: &store.events, validation: []byte(report)})
	if _, err := updater.Run(context.Background(), "geofabrik:norcal"); err == nil || !strings.Contains(err.Error(), "invalidStateParkAreas") {
		t.Fatalf("expected state park validation error, got %v", err)
	}
	if store.promoted {
		t.Fatal("candidate with an invalid state park was promoted")
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

func TestCommandPipelineVersionProbesAreQuiet(t *testing.T) {
	var log bytes.Buffer
	pipeline := CommandPipeline{Log: &log, outputCommand: func(_ context.Context, _ []string, name string, _ ...string) (string, error) {
		switch name {
		case "osmium":
			return "osmium version 1.19.0 (v1.19.0)", nil
		case "osm2pgsql":
			return "osm2pgsql version 2.3.1 (2.3.1)", nil
		default:
			return "", fmt.Errorf("unexpected command %s", name)
		}
	}}
	versions, err := pipeline.Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if versions != (ToolVersions{Osmium: ExpectedOsmium, Osm2pgsql: ExpectedOsm2pgsql}) {
		t.Fatalf("versions=%+v", versions)
	}
	if log.Len() != 0 {
		t.Fatalf("version probes wrote to log: %q", log.String())
	}
}

func TestOsm2pgsqlProgressCarriageReturnsBecomeLogLines(t *testing.T) {
	var log bytes.Buffer
	writer := &carriageReturnLineWriter{w: &log}
	for _, chunk := range [][]byte{
		[]byte("Processing: Node(1k)\rProcessing: Node(2k)\r"),
		[]byte("\nDone\r\n"),
	} {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	want := "Processing: Node(1k)\nProcessing: Node(2k)\nDone\n"
	if log.String() != want {
		t.Fatalf("normalized progress=%q, want %q", log.String(), want)
	}
}

func TestCommandPipelinePropagationMaintenanceVacuumsComponentTable(t *testing.T) {
	var commandName string
	var arguments []string
	pipeline := CommandPipeline{DatabaseURL: "postgresql://database/osm", outputCommand: func(_ context.Context, _ []string, name string, args ...string) (string, error) {
		commandName, arguments = name, append([]string(nil), args...)
		return "", nil
	}}
	if err := pipeline.Maintain(context.Background(), "identity-propagate", Generation{ID: 56, SchemaName: "osm_build_56"}); err != nil {
		t.Fatal(err)
	}
	if commandName != "psql" || !contains(arguments, `VACUUM "osm_build_56".attribution_identity_components`) {
		t.Fatalf("maintenance command=%s args=%v", commandName, arguments)
	}
}

func TestEveryTopLevelOSMSQLFileImplementsAnActiveStage(t *testing.T) {
	entries, err := os.ReadDir("../../osm")
	if err != nil {
		t.Fatal(err)
	}
	active := make(map[string]string, len(pipelineSQLFiles))
	for stage, name := range pipelineSQLFiles {
		if previous, duplicate := active[name]; duplicate {
			t.Errorf("SQL file %s is mapped by both %s and %s", name, previous, stage)
		}
		active[name] = stage
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		if _, ok := active[entry.Name()]; !ok {
			t.Errorf("top-level OSM SQL file %s is not mapped to an active stage", entry.Name())
		}
	}
	for name, stage := range active {
		if _, err := os.Stat(filepath.Join("../../osm", name)); err != nil {
			t.Errorf("active stage %s references missing SQL file %s: %v", stage, name, err)
		}
	}
}

func TestSafeFailureRedactsAndBoundsSummary(t *testing.T) {
	message := safeFailure(errors.New("connect postgresql://user:secret@database/db\n" + strings.Repeat("x", 600)))
	if strings.Contains(message, "secret") || strings.Contains(message, "\n") || len([]rune(message)) > 512 {
		t.Fatalf("unsafe summary %q", message)
	}
}

func TestCommandPipelineUsesAtomicOrBatchedSQLTransactions(t *testing.T) {
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
	for _, stage := range []string{"postprocess", "derive", "clip-finalize", "identity-components", "prepare-partitions"} {
		if _, err := pipeline.Run(context.Background(), stage, generation, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pipeline.Run(context.Background(), "validate", generation, "", ""); err != nil {
		t.Fatal(err)
	}
	for index, args := range calls {
		hasSingleTransaction := contains(args, "--single-transaction")
		wantSingleTransaction := index == 0 || index == 2 || index == 4
		if hasSingleTransaction != wantSingleTransaction {
			t.Errorf("mutating call %d omitted --single-transaction: %v", index, args)
		}
		hasAtomicCheckpoint := contains(args, "--command")
		wantAtomicCheckpoint := index == 0 || index == 2 || index == 4
		if hasAtomicCheckpoint != wantAtomicCheckpoint {
			t.Errorf("call %d atomic checkpoint=%v, want %v: %v", index, hasAtomicCheckpoint, wantAtomicCheckpoint, args)
		}
	}
	if got := calls[3]; !contains(got, "--file") || !contains(got, "/pipeline/identity-components.sql") {
		t.Fatalf("identity-components args = %v", got)
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
	if !strings.Contains(message, "Running OSM tool sh") ||
		!strings.Contains(message, "OSM tool sh still running") ||
		!strings.Contains(message, "progress-output") {
		t.Fatalf("missing streamed progress in %q", message)
	}
}

func TestCommandPipelineLogsRepeatedBatchSize(t *testing.T) {
	var log bytes.Buffer
	pipeline := CommandPipeline{DatabaseURL: "postgresql://database/osm", Root: "/pipeline", Log: &log, progressInterval: 5 * time.Millisecond,
		outputCommand: func(context.Context, []string, string, ...string) (string, error) {
			time.Sleep(15 * time.Millisecond)
			return "", nil
		}}
	_, err := pipeline.Run(context.Background(), "clip-apply", Generation{
		ID: 42, RegionID: "geofabrik:norcal", SchemaName: "osm_build_42", BatchSize: 18000,
	}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "Running OSM batch clip-apply (batch size: 18000)\n") {
		t.Fatalf("missing batch size in log %q", log.String())
	}
	if !strings.Contains(log.String(), "OSM batch clip-apply still running") ||
		strings.Contains(log.String(), "OSM batch clip-apply (batch size: 18000) still running") {
		t.Fatalf("unexpected heartbeat label in log %q", log.String())
	}
}

func TestRepeatedBatchSizeUsesConfiguredOverridesAndDurableCursor(t *testing.T) {
	if size := repeatedBatchSize("clip-residual", json.RawMessage(`{"batch_size":100000}`)); size != 10000 {
		t.Fatalf("clip-residual batch size=%d, want 10000", size)
	}
	if size := repeatedBatchSize("identity-components", json.RawMessage(`{"batch_size":500000}`)); size != 500000 {
		t.Fatalf("identity-components batch size=%d, want 500000", size)
	}
	if size := repeatedBatchSize("identity-propagate", json.RawMessage(`{"phase":"hook","batch_size":400000}`)); size != 100000 {
		t.Fatalf("identity-propagate/hook batch size=%d, want 100000", size)
	}
	if size := repeatedBatchSize("identity-propagate", json.RawMessage(`{"phase":"compress","batch_size":1000000}`)); size != 1000000 {
		t.Fatalf("identity-propagate/compress batch size=%d, want 1000000", size)
	}
	if size := repeatedBatchSize("clip-candidates", json.RawMessage(`{}`)); size != 200000 {
		t.Fatalf("initial clip-candidates batch size=%d, want 200000", size)
	}
}

func TestRepeatedBatchProgressAndBannerSuffix(t *testing.T) {
	index, total := repeatedBatchProgress(json.RawMessage(`{"batch_index":1,"batch_total":25}`))
	if index != 1 || total != 25 {
		t.Fatalf("batch progress=(%d,%d), want (1,25)", index, total)
	}
	generation := Generation{BatchSize: 1000000, BatchIndex: index, BatchTotal: total}
	if suffix := batchProgressSuffix(generation); suffix != " (1 of 25)" {
		t.Fatalf("batch progress suffix=%q", suffix)
	}
	for _, cursor := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"batch_index":0,"batch_total":25}`),
		json.RawMessage(`{"batch_index":26,"batch_total":25}`),
	} {
		index, total = repeatedBatchProgress(cursor)
		if index != 0 || total != 0 {
			t.Fatalf("invalid cursor %s produced progress (%d,%d)", cursor, index, total)
		}
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
			if _, err := pipeline.Run(test.ctx(), "identity-components", generation, "", ""); err == nil {
				t.Fatal("expected failure")
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1", calls)
			}
		})
	}
}

func TestCommandPipelineDoesNotRetryAmbiguousBatchCommit(t *testing.T) {
	calls := 0
	pipeline := CommandPipeline{DatabaseURL: "postgresql://database/osm", Root: "/pipeline",
		retryDelays: []time.Duration{0, 0, 0, 0}, outputCommand: func(context.Context, []string, string, ...string) (string, error) {
			calls++
			return "", errors.New("connection to server was lost")
		}}
	_, err := pipeline.Run(context.Background(), "clip-candidates", Generation{ID: 42, RegionID: "geofabrik:norcal", SchemaName: "osm_build_42"}, "", "")
	if err == nil || calls != 1 {
		t.Fatalf("ambiguous batch commit error=%v calls=%d, want one attempt", err, calls)
	}
}

func TestSingleAttemptBatchLogOmitsAttemptFraction(t *testing.T) {
	var log bytes.Buffer
	pipeline := CommandPipeline{DatabaseURL: "postgresql://database/osm", Root: "/pipeline", Log: &log,
		outputCommand: func(_ context.Context, _ []string, _ string, _ ...string) (string, error) {
			return "", errors.New("syntax error")
		}}
	_, _ = pipeline.Run(context.Background(), "clip-candidates", Generation{ID: 42, RegionID: "geofabrik:norcal", SchemaName: "osm_build_42"}, "", "")
	if strings.Contains(log.String(), "attempt 1/1") {
		t.Fatalf("single-attempt batch log contains redundant attempt fraction: %q", log.String())
	}
}

func sqlStage(args []string) string {
	for index, arg := range args {
		if arg == "--file" && index+1 < len(args) {
			stage := strings.TrimSuffix(filepath.Base(args[index+1]), ".sql")
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
