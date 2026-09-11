package osm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	ImporterVersion   = 1
	DerivationVersion = 2
	ExpectedOsmium    = "1.19.0"
	ExpectedOsm2pgsql = "2.3.1"
)

type Generation struct {
	ID         int64
	RegionID   string
	SchemaName string
	SourceURL  string
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

type RegionDownloader func(context.Context, *http.Client, Region, string, int64) (DownloadResult, error)

type Updater struct {
	Store        UpdateStore
	Pipeline     UpdatePipeline
	HTTPClient   *http.Client
	Download     RegionDownloader
	MaximumBytes int64
	ScratchRoot  string
}

var pipelineStages = []string{
	"tags-filter", "check-refs", "osm2pgsql", "postprocess", "derive", "clip",
	"prepare-partitions", "validate",
}

var databaseURLPattern = regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s]+`)

func (u Updater) Run(ctx context.Context, regionID string) (generation Generation, err error) {
	if u.Store == nil || u.Pipeline == nil || u.HTTPClient == nil || u.Download == nil || u.MaximumBytes < 1 || u.ScratchRoot == "" {
		return Generation{}, errors.New("invalid OSM updater configuration")
	}
	versions, err := u.Pipeline.Versions(ctx)
	if err != nil {
		return Generation{}, fmt.Errorf("validate importer tools: %w", err)
	}
	if versions.Osmium != ExpectedOsmium || versions.Osm2pgsql != ExpectedOsm2pgsql {
		return Generation{}, fmt.Errorf("unexpected importer versions: osmium=%q osm2pgsql=%q", versions.Osmium, versions.Osm2pgsql)
	}
	// Retry cleanup left by an earlier successful promotion before reserving new work.
	if err := u.Store.ProcessStorageGC(ctx); err != nil {
		return Generation{}, fmt.Errorf("process pending OSM storage GC: %w", err)
	}
	generation, err = u.Store.Reserve(ctx, regionID, versions)
	if err != nil {
		return Generation{}, fmt.Errorf("reserve OSM generation: %w", err)
	}
	scratch := filepath.Join(u.ScratchRoot, fmt.Sprintf("generation-%d", generation.ID))
	if err := os.MkdirAll(scratch, 0700); err != nil {
		updateErr := fmt.Errorf("create generation scratch: %w", err)
		failErr := u.Store.Fail(context.WithoutCancel(ctx), generation, safeFailure(updateErr))
		dropErr := u.Store.DropBuildSchema(context.WithoutCancel(ctx), generation.SchemaName)
		return generation, errors.Join(updateErr, failErr, dropErr)
	}
	promoted := false
	defer func() {
		removeErr := os.RemoveAll(scratch)
		if err == nil || promoted {
			if removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove generation scratch: %w", removeErr))
			}
			return
		}
		failErr := u.Store.Fail(context.WithoutCancel(ctx), generation, safeFailure(err))
		dropErr := u.Store.DropBuildSchema(context.WithoutCancel(ctx), generation.SchemaName)
		err = errors.Join(err, failErr, dropErr, removeErr)
	}()

	sourcePBF := filepath.Join(scratch, "source.osm.pbf")
	filteredPBF := filepath.Join(scratch, "filtered.osm.pbf")
	region := Region{ID: generation.RegionID, SourceURL: generation.SourceURL}
	download, err := u.Download(ctx, u.HTTPClient, region, sourcePBF, u.MaximumBytes)
	if err != nil {
		return generation, fmt.Errorf("download source PBF: %w", err)
	}
	sourceTimestamp, err := u.Pipeline.SourceTimestamp(ctx, sourcePBF)
	if err != nil {
		return generation, fmt.Errorf("read source PBF header: %w", err)
	}
	if err := u.Store.RecordDownload(ctx, generation.ID, download, sourceTimestamp); err != nil {
		return generation, fmt.Errorf("record source PBF: %w", err)
	}

	var validation json.RawMessage
	for _, stage := range pipelineStages {
		output, stageErr := u.Pipeline.Run(ctx, stage, generation, sourcePBF, filteredPBF)
		if stageErr != nil {
			return generation, fmt.Errorf("OSM stage %s: %w", stage, stageErr)
		}
		if stage == "validate" {
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
		"missingEndpointIndexes": 0,
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
