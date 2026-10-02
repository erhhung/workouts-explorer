package osm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	ImporterVersion   = 3
	DerivationVersion = 20
	ExpectedOsmium    = "1.19.0"
	ExpectedOsm2pgsql = "2.3.1"
)

type Generation struct {
	ID         int64
	RegionID   string
	SchemaName string
	SourceURL  string
	BatchSize  int
	BatchIndex int
	BatchTotal int
}

type ToolVersions struct {
	Osmium    string
	Osm2pgsql string
}

type UpdateStore interface {
	Reserve(context.Context, string, ToolVersions) (Generation, error)
	RecordDownload(context.Context, int64, DownloadResult, time.Time) error
	SetValidating(context.Context, int64, json.RawMessage) error
	Promote(context.Context, Generation, json.RawMessage) error
	Fail(context.Context, Generation, string) error
	DropBuildSchema(context.Context, string) error
	ProcessStorageGC(context.Context) error
}

type UpdatePipeline interface {
	Versions(context.Context) (ToolVersions, error)
	SourceTimestamp(context.Context, string) (time.Time, error)
	Run(context.Context, string, Generation, string, string) ([]byte, error)
}

type StageCheckpointStore interface {
	Resume(context.Context, int64, string, ToolVersions) (Generation, map[string]bool, error)
	StartStage(context.Context, int64, string, int, string) error
	StageCompleted(context.Context, int64, string) (bool, error)
	StageFenceMatches(context.Context, int64, string, string) (bool, error)
	CompleteStage(context.Context, int64, string) error
	FailStage(context.Context, Generation, string, string) error
	LockGeneration(context.Context, int64) (func(), error)
}
type StageFencer interface{ StageFence(string) (string, error) }
type StageCursorStore interface {
	StageCursor(context.Context, int64, string) (json.RawMessage, error)
}
type StageMaintainer interface {
	Maintain(context.Context, string, Generation) error
}

type StorageBlockStore interface {
	RecordStorageBlock(context.Context, Generation, string, *StoragePreflightError) error
}

type UpdaterLocker interface {
	LockUpdater(context.Context) (func(), error)
}

type StoragePreflight interface {
	Check(context.Context) (StorageObservation, error)
}

type StorageObservation struct {
	CheckedAt         time.Time
	SampledAt         time.Time
	PostgresPrimary   string
	Namespace         string
	PodName           string
	NodeName          string
	PVCName           string
	CapacityBytes     int64
	FreeBytes         int64
	RequiredFreeBytes int64
}

type RegionDownloader func(context.Context, *http.Client, Region, string, int64) (DownloadResult, error)

type Updater struct {
	Store               UpdateStore
	Pipeline            UpdatePipeline
	HTTPClient          *http.Client
	Download            RegionDownloader
	MaximumBytes        int64
	ScratchRoot         string
	ResumeGenerationID  int64
	StoragePreflight    StoragePreflight
	Log                 io.Writer
	LogLocation         *time.Location
	Now                 func() time.Time
	StoragePollInterval time.Duration
	StorageFreshTimeout time.Duration
}

var identityPipelineStages = []string{
	"attribute-slivers", "identity-segments", "identity-edges", "identity-proximity",
	"identity-components", "identity-propagate", "identity-label-overrides", "identity-rewrite",
}

var pipelineStages = func() []string {
	stages := []string{
		"tags-filter", "check-refs", "osm2pgsql", "postprocess", "derive", "clip-candidates", "clip-replacements",
		"clip-apply", "clip-residual", "clip-finalize", "attribute-parks-tags", "attribute-education",
	}
	stages = append(stages, identityPipelineStages...)
	return append(stages, "prepare-partitions", "validate")
}()

var batchStages = map[string]bool{
	"clip-candidates":          true,
	"derive":                   true,
	"clip-replacements":        true,
	"clip-apply":               true,
	"clip-residual":            true,
	"attribute-parks-tags":     true,
	"attribute-education":      true,
	"attribute-slivers":        true,
	"identity-segments":        true,
	"identity-edges":           true,
	"identity-proximity":       true,
	"identity-components":      true,
	"identity-propagate":       true,
	"identity-label-overrides": true,
	"identity-rewrite":         true,
	"validate":                 true,
}

var initialBatchSizes = map[string]int{
	"derive":                   150000,
	"clip-candidates":          200000,
	"clip-replacements":        7500,
	"clip-apply":               18000,
	"clip-residual":            10000,
	"attribute-parks-tags":     750000,
	"attribute-education":      750000,
	"attribute-slivers":        500000,
	"identity-segments":        500000,
	"identity-edges":           500000,
	"identity-proximity":       500000,
	"identity-components":      750000,
	"identity-propagate":       1000000,
	"identity-label-overrides": 250000,
	"identity-rewrite":         250000,
}

var batchSizeOverrides = map[string]int{
	"clip-apply":              initialBatchSizes["clip-apply"],
	"clip-residual":           initialBatchSizes["clip-residual"],
	"identity-propagate/hook": 100000,
}

func repeatedBatchSize(stage string, cursor json.RawMessage) int {
	var fields struct {
		BatchSize int    `json:"batch_size"`
		Phase     string `json:"phase"`
	}
	_ = json.Unmarshal(cursor, &fields)
	if size := batchSizeOverrides[stage+"/"+fields.Phase]; size > 0 {
		return size
	}
	if size := batchSizeOverrides[stage]; size > 0 {
		return size
	}
	if fields.BatchSize > 0 {
		return fields.BatchSize
	}
	return initialBatchSizes[stage]
}

func repeatedBatchProgress(cursor json.RawMessage) (int, int) {
	var fields struct {
		BatchIndex int `json:"batch_index"`
		BatchTotal int `json:"batch_total"`
	}
	if json.Unmarshal(cursor, &fields) != nil || fields.BatchIndex <= 0 || fields.BatchTotal < fields.BatchIndex {
		return 0, 0
	}
	return fields.BatchIndex, fields.BatchTotal
}

func batchProgressSuffix(generation Generation) string {
	if generation.BatchSize <= 0 || generation.BatchIndex <= 0 || generation.BatchTotal < generation.BatchIndex {
		return ""
	}
	return fmt.Sprintf(" (%d of %d)", generation.BatchIndex, generation.BatchTotal)
}

var atomicSQLStages = map[string]bool{
	"postprocess":        true,
	"clip-finalize":      true,
	"prepare-partitions": true,
}

func sqlCheckpointStage(stage string) bool { return batchStages[stage] || atomicSQLStages[stage] }

func stageNeedsStoragePreflight(stage string) bool {
	switch stage {
	case "postprocess", "derive", "clip-candidates", "clip-replacements", "clip-apply", "clip-residual", "clip-finalize", "attribute-parks-tags", "attribute-education", "attribute-slivers", "identity-segments", "identity-edges", "identity-proximity", "identity-components", "identity-propagate", "identity-label-overrides", "identity-rewrite", "prepare-partitions", "validate":
		return true
	default:
		return false
	}
}

func (u Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u Updater) logBanner(format string, arguments ...any) {
	if u.Log == nil {
		return
	}
	location := u.LogLocation
	if location == nil {
		location = time.Local
	}
	_, _ = fmt.Fprintf(u.Log, "%s ====== %s ======\n", u.now().In(location).Format("2006-01-02 15:04:05"), fmt.Sprintf(format, arguments...))
}

func (u Updater) logCompletionBanner(format string, arguments ...any) {
	u.logBanner(format, arguments...)
	if u.Log != nil {
		_, _ = fmt.Fprintln(u.Log)
	}
}

func storageGiB(value int64) string {
	return strconv.FormatFloat(float64(value)/(1024*1024*1024), 'f', 2, 64)
}

func storagePercent(observation StorageObservation) int64 {
	if observation.CapacityBytes <= 0 {
		return 0
	}
	return observation.FreeBytes * 100 / observation.CapacityBytes
}

func storageBelowPercent(observation StorageObservation, percent int64) bool {
	return observation.CapacityBytes > 0 && observation.FreeBytes*100 < observation.CapacityBytes*percent
}

func (u Updater) logStorageStart(observation StorageObservation) {
	if u.Log == nil {
		return
	}
	_, _ = fmt.Fprintf(u.Log, "PVC: %s (%s) | capacity: %s GiB | free: %s GiB (%d%%)\n",
		observation.PVCName, observation.NodeName, storageGiB(observation.CapacityBytes),
		storageGiB(observation.FreeBytes), storagePercent(observation))
}

func (u Updater) logStorageCompletion(preGC, postGC StorageObservation) {
	if u.Log == nil {
		return
	}
	_, _ = fmt.Fprintf(u.Log, "PVC: %s (%s) | pre-GC: %s GiB | post-GC: %s GiB (%d%%)\n",
		postGC.PVCName, postGC.NodeName, storageGiB(preGC.FreeBytes), storageGiB(postGC.FreeBytes), storagePercent(postGC))
}

func (u Updater) monitorStorage(ctx context.Context, initial StorageObservation) (context.CancelFunc, <-chan StorageObservation) {
	monitorContext, cancel := context.WithCancel(ctx)
	result := make(chan StorageObservation, 1)
	interval := u.StoragePollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		minimum := initial
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(result)
		for {
			select {
			case <-monitorContext.Done():
				result <- minimum
				return
			case <-ticker.C:
				observation, err := u.StoragePreflight.Check(monitorContext)
				if err != nil {
					var blocked *StoragePreflightError
					if errors.As(err, &blocked) {
						observation = blocked.Observation
					}
				}
				if observation.PVCName == minimum.PVCName && !observation.SampledAt.IsZero() && observation.FreeBytes < minimum.FreeBytes {
					minimum = observation
				}
			}
		}
	}()
	return cancel, result
}

func (u Updater) freshStorageObservation(ctx context.Context, after time.Time) (StorageObservation, error) {
	timeout := u.StorageFreshTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		observation, err := u.StoragePreflight.Check(ctx)
		if err != nil {
			var blocked *StoragePreflightError
			if !errors.As(err, &blocked) || blocked.Observation.PVCName == "" {
				return observation, err
			}
			observation = blocked.Observation
		}
		if observation.SampledAt.IsZero() || observation.SampledAt.After(after) {
			return observation, nil
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return StorageObservation{}, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return StorageObservation{}, errors.New("timed out waiting for fresh post-batch PVC metrics")
		case <-timer.C:
		}
	}
}

func (u Updater) postGCStorageObservation(ctx context.Context, lowWater StorageObservation, batchFinished time.Time) (StorageObservation, error) {
	observation, err := u.freshStorageObservation(ctx, batchFinished)
	if err != nil {
		return observation, err
	}
	if observation.FreeBytes >= lowWater.FreeBytes {
		return observation, nil
	}
	after := observation.SampledAt
	if after.IsZero() {
		after = batchFinished
	}
	return u.freshStorageObservation(ctx, after)
}

var databaseURLPattern = regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s]+`)

func (u Updater) Run(ctx context.Context, regionID string) (generation Generation, err error) {
	updateStarted := u.now()
	if u.Store == nil || u.Pipeline == nil || u.HTTPClient == nil || u.Download == nil || u.StoragePreflight == nil || u.MaximumBytes < 1 || u.ScratchRoot == "" {
		return Generation{}, errors.New("invalid OSM updater configuration")
	}
	versions := ToolVersions{Osmium: ExpectedOsmium, Osm2pgsql: ExpectedOsm2pgsql}
	if u.ResumeGenerationID == 0 {
		versions, err = u.Pipeline.Versions(ctx)
		if err != nil {
			return Generation{}, fmt.Errorf("validate importer tools: %w", err)
		}
		if versions.Osmium != ExpectedOsmium || versions.Osm2pgsql != ExpectedOsm2pgsql {
			return Generation{}, fmt.Errorf("unexpected importer versions: osmium=%q osm2pgsql=%q", versions.Osmium, versions.Osm2pgsql)
		}
	}
	updaterLocker, ok := u.Store.(UpdaterLocker)
	if !ok {
		return Generation{}, errors.New("OSM store does not support exclusive updater locking")
	}
	unlockUpdater, err := updaterLocker.LockUpdater(ctx)
	if err != nil {
		return Generation{}, fmt.Errorf("acquire exclusive OSM updater ownership: %w", err)
	}
	defer unlockUpdater()
	// Retry cleanup left by an earlier successful promotion before reserving new work.
	if err := u.Store.ProcessStorageGC(ctx); err != nil {
		return Generation{}, fmt.Errorf("process pending OSM storage GC: %w", err)
	}
	completed := map[string]bool{}
	checkpointStore, resumable := u.Store.(StageCheckpointStore)
	var unlock func()
	if u.ResumeGenerationID > 0 {
		if !resumable {
			return Generation{}, errors.New("OSM store does not support resume")
		}
		unlock, err = checkpointStore.LockGeneration(ctx, u.ResumeGenerationID)
		if err != nil {
			return Generation{}, err
		}
		generation, completed, err = checkpointStore.Resume(ctx, u.ResumeGenerationID, regionID, versions)
		if err != nil {
			unlock()
			return Generation{}, fmt.Errorf("resume OSM generation: %w", err)
		}
	} else {
		generation, err = u.Store.Reserve(ctx, regionID, versions)
		if err != nil {
			return Generation{}, fmt.Errorf("reserve OSM generation: %w", err)
		}
		if resumable {
			unlock, err = checkpointStore.LockGeneration(ctx, generation.ID)
			if err != nil {
				failErr := u.Store.Fail(context.WithoutCancel(ctx), generation, safeFailure(err))
				dropErr := u.Store.DropBuildSchema(context.WithoutCancel(ctx), generation.SchemaName)
				return generation, errors.Join(err, failErr, dropErr)
			}
		}
	}
	if unlock != nil {
		defer unlock()
	}
	scratch := filepath.Join(u.ScratchRoot, fmt.Sprintf("generation-%d", generation.ID))
	if err := os.MkdirAll(scratch, 0700); err != nil {
		updateErr := fmt.Errorf("create generation scratch: %w", err)
		failErr := u.Store.Fail(context.WithoutCancel(ctx), generation, safeFailure(updateErr))
		dropErr := u.Store.DropBuildSchema(context.WithoutCancel(ctx), generation.SchemaName)
		return generation, errors.Join(updateErr, failErr, dropErr)
	}
	promoted := false
	preserveBuild := completed["osm2pgsql"]
	defer func() {
		removeErr := os.RemoveAll(scratch)
		if err == nil || promoted {
			if removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove generation scratch: %w", removeErr))
			}
			return
		}
		if preserveBuild {
			err = errors.Join(err, removeErr)
			return
		}
		failErr := u.Store.Fail(context.WithoutCancel(ctx), generation, safeFailure(err))
		dropErr := u.Store.DropBuildSchema(context.WithoutCancel(ctx), generation.SchemaName)
		err = errors.Join(err, failErr, dropErr, removeErr)
	}()

	sourcePBF := filepath.Join(scratch, "source.osm.pbf")
	filteredPBF := filepath.Join(scratch, "filtered.osm.pbf")
	if u.ResumeGenerationID == 0 {
		downloadStarted := u.now()
		u.logBanner("Starting OSM update generation %d (derivation v%d) batch %q", generation.ID, DerivationVersion, "download")
		region := Region{ID: generation.RegionID, SourceURL: generation.SourceURL}
		download, downloadErr := u.Download(ctx, u.HTTPClient, region, sourcePBF, u.MaximumBytes)
		if downloadErr != nil {
			return generation, fmt.Errorf("download source PBF: %w", downloadErr)
		}
		sourceTimestamp, timestampErr := u.Pipeline.SourceTimestamp(ctx, sourcePBF)
		if timestampErr != nil {
			return generation, fmt.Errorf("read source PBF header: %w", timestampErr)
		}
		if recordErr := u.Store.RecordDownload(ctx, generation.ID, download, sourceTimestamp); recordErr != nil {
			return generation, fmt.Errorf("record source PBF: %w", recordErr)
		}
		u.logCompletionBanner("OSM update generation %d (derivation v%d) batch %q completed in %s", generation.ID, DerivationVersion, "download", u.now().Sub(downloadStarted).Round(time.Second))
	}

	var validation json.RawMessage
	for stageOrder, stageName := range pipelineStages {
		if completed[stageName] && stageName != "validate" {
			if fencer, ok := u.Pipeline.(StageFencer); ok {
				fence, fenceErr := fencer.StageFence(stageName)
				if fenceErr != nil {
					return generation, fenceErr
				}
				matches, fenceErr := checkpointStore.StageFenceMatches(ctx, generation.ID, stageName, fence)
				if fenceErr != nil || !matches {
					if fenceErr == nil {
						fenceErr = fmt.Errorf("completed stage %s fence changed; abort and rebuild the generation", stageName)
					}
					return generation, fenceErr
				}
			}
			continue
		}
		if resumable {
			fence := strings.Repeat("0", 64)
			if fencer, ok := u.Pipeline.(StageFencer); ok {
				fence, err = fencer.StageFence(stageName)
				if err != nil {
					return generation, err
				}
			}
			if err := checkpointStore.StartStage(ctx, generation.ID, stageName, stageOrder, fence); err != nil {
				return generation, err
			}
		}
		var output []byte
		for {
			unitStarted := u.now()
			batchName := stageName
			generation.BatchSize = 0
			generation.BatchIndex = 0
			generation.BatchTotal = 0
			if cursorStore, ok := u.Store.(StageCursorStore); ok {
				cursor, cursorErr := cursorStore.StageCursor(ctx, generation.ID, stageName)
				if cursorErr != nil {
					return generation, cursorErr
				}
				var cursorFields struct {
					Phase string `json:"phase"`
				}
				if json.Unmarshal(cursor, &cursorFields) == nil && cursorFields.Phase != "" {
					batchName += "/" + cursorFields.Phase
				}
				if batchStages[stageName] {
					generation.BatchSize = repeatedBatchSize(stageName, cursor)
					generation.BatchIndex, generation.BatchTotal = repeatedBatchProgress(cursor)
				}
			}
			u.logBanner("Starting OSM update generation %d (derivation v%d) batch %q%s", generation.ID, DerivationVersion, batchName, batchProgressSuffix(generation))
			var storageStart StorageObservation
			var stopStorageMonitor context.CancelFunc
			var storageMinimum <-chan StorageObservation
			if u.StoragePreflight != nil && stageNeedsStoragePreflight(stageName) {
				storageStart, err = u.StoragePreflight.Check(ctx)
				if err != nil {
					var blocked *StoragePreflightError
					if errors.As(err, &blocked) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						blockStore, ok := u.Store.(StorageBlockStore)
						if !ok {
							return generation, fmt.Errorf("OSM stage %s storage preflight cannot be recorded: %w", stageName, err)
						}
						if recordErr := blockStore.RecordStorageBlock(context.WithoutCancel(ctx), generation, stageName, blocked); recordErr != nil {
							return generation, errors.Join(fmt.Errorf("OSM stage %s storage preflight: %w", stageName, err), recordErr)
						}
					}
					return generation, fmt.Errorf("OSM stage %s storage preflight: %w", stageName, err)
				}
				if !storageStart.CheckedAt.IsZero() {
					u.logStorageStart(storageStart)
					stopStorageMonitor, storageMinimum = u.monitorStorage(ctx, storageStart)
				}
			}
			output, err = u.Pipeline.Run(ctx, stageName, generation, sourcePBF, filteredPBF)
			storageLow := storageStart
			if stopStorageMonitor != nil {
				stopStorageMonitor()
				storageLow = <-storageMinimum
			}
			if err == nil && stageName == "identity-propagate" && storageBelowPercent(storageLow, 15) {
				if maintainer, ok := u.Pipeline.(StageMaintainer); ok {
					err = maintainer.Maintain(ctx, stageName, generation)
				}
			}
			batchFinished := u.now()
			if err != nil {
				if resumable && (completed["osm2pgsql"] || stageName != "tags-filter" && stageName != "check-refs" && stageName != "osm2pgsql") {
					_ = checkpointStore.FailStage(context.WithoutCancel(ctx), generation, stageName, safeFailure(err))
				}
				return generation, fmt.Errorf("OSM stage %s: %w", stageName, err)
			}
			if !storageStart.CheckedAt.IsZero() {
				storageEnd, storageErr := u.postGCStorageObservation(ctx, storageLow, batchFinished)
				if storageErr != nil {
					return generation, fmt.Errorf("OSM stage %s post-batch storage observation: %w", stageName, storageErr)
				}
				u.logStorageCompletion(storageLow, storageEnd)
			}
			u.logCompletionBanner("OSM update generation %d (derivation v%d) batch %q%s completed in %s", generation.ID, DerivationVersion, batchName, batchProgressSuffix(generation), batchFinished.Sub(unitStarted).Round(time.Second))
			if !resumable || !sqlCheckpointStage(stageName) {
				break
			}
			done, completedErr := checkpointStore.StageCompleted(ctx, generation.ID, stageName)
			if completedErr != nil {
				return generation, completedErr
			}
			if done {
				break
			}
			if atomicSQLStages[stageName] {
				return generation, fmt.Errorf("OSM stage %s did not commit its terminal checkpoint", stageName)
			}
		}
		if resumable {
			if !sqlCheckpointStage(stageName) {
				if err := checkpointStore.CompleteStage(ctx, generation.ID, stageName); err != nil {
					return generation, err
				}
			}
			completed[stageName] = true
			if stageName == "osm2pgsql" {
				preserveBuild = true
			}
		}
		if stageName == "validate" {
			validation, err = validateReport(output, generation)
			if err != nil {
				return generation, fmt.Errorf("OSM validation gate: %w", err)
			}
		}
	}
	if err := u.Store.SetValidating(ctx, generation.ID, validation); err != nil {
		return generation, fmt.Errorf("set generation validating: %w", err)
	}
	if err := u.Store.Promote(ctx, generation, validation); err != nil {
		return generation, fmt.Errorf("promote OSM generation: %w", err)
	}
	promoted = true
	if err := u.Store.ProcessStorageGC(ctx); err != nil {
		return generation, fmt.Errorf("generation %d promoted; storage GC remains retryable: %w", generation.ID, err)
	}
	u.logCompletionBanner("OSM update generation %d completed in %s", generation.ID, u.now().Sub(updateStarted).Round(time.Second))
	return generation, nil
}

func validateReport(output []byte, generation Generation) (json.RawMessage, error) {
	output = []byte(strings.TrimSpace(string(output)))
	var report map[string]any
	if len(output) == 0 || json.Unmarshal(output, &report) != nil {
		return nil, errors.New("validation did not return one JSON object")
	}
	required := map[string]float64{
		"preparedGenerationId": float64(generation.ID), "provenanceMismatches": 0,
		"importerVersion": ImporterVersion, "derivationVersion": DerivationVersion,
		"sourceVersionMismatches": 0, "logicalPathMismatches": 0, "invalidWays": 0,
		"invalidPathSegments": 0, "orphanPathSegments": 0, "materialLocalityResiduals": 0,
		"invalidParkAttributions": 0, "materialParkResiduals": 0,
		"invalidEducationAttributions": 0, "materialEducationResiduals": 0,
		"invalidNationalParkAreas":            0,
		"invalidStateParkAreas":               0,
		"remainingConnectedAttributionSplits": 0,
		"missingEndpointIndexes":              0,
	}
	if report["partitionPrepared"] != true || report["preparedRegionId"] != generation.RegionID {
		return nil, errors.New("candidate is not partition-prepared for this generation")
	}
	for key, expected := range required {
		if report[key] != expected {
			return nil, fmt.Errorf("%s is %v, expected %v", key, report[key], expected)
		}
	}
	for _, key := range []string{"ways", "pathSegments", "logicalPaths"} {
		value, ok := report[key].(float64)
		if !ok || value <= 0 {
			return nil, fmt.Errorf("%s must be positive", key)
		}
	}
	return append(json.RawMessage(nil), output...), nil
}

func safeFailure(err error) string {
	message := databaseURLPattern.ReplaceAllString(err.Error(), "[redacted database URL]")
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 512 {
		message = string(runes[:512])
	}
	return message
}
