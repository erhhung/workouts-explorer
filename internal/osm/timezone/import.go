package timezone

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Provider        = "timezone-boundary-builder"
	maxArchiveBytes = 256 << 20
)

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	tzidPattern   = regexp.MustCompile(`^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+)+$`)
)

type Options struct {
	ArchivePath string
	Release     string
	SourceURL   string
	SHA256      string
}

type Result struct {
	DatasetID    int64
	FeatureCount int64
}

// Validate checks all caller-controlled provenance before opening a database transaction.
func (options Options) Validate() error {
	if strings.TrimSpace(options.ArchivePath) == "" {
		return errors.New("timezone boundary archive path is required")
	}
	if options.Release != strings.TrimSpace(options.Release) || len(options.Release) < 1 || len(options.Release) > 128 {
		return errors.New("timezone boundary release must be 1 to 128 non-surrounding-whitespace characters")
	}
	parsed, err := url.Parse(options.SourceURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("timezone boundary source URL must be an absolute HTTPS URL without user info")
	}
	if !sha256Pattern.MatchString(options.SHA256) {
		return errors.New("timezone boundary SHA256 must be exactly 64 lowercase hexadecimal characters")
	}
	return nil
}

// DownloadArchive retrieves a pinned artifact for import. Runtime timezone
// resolution remains fully offline after the artifact is stored in PostGIS.
func DownloadArchive(ctx context.Context, sourceURL, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("create timezone boundary download: %w", err)
	}
	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(request)
	if err != nil {
		return fmt.Errorf("download timezone boundary archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download timezone boundary archive: unexpected HTTP status %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create timezone boundary archive: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, maxArchiveBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > maxArchiveBytes {
		_ = os.Remove(destination)
		if written > maxArchiveBytes {
			return errors.New("timezone boundary archive exceeds 256 MiB")
		}
		if copyErr != nil {
			return fmt.Errorf("write timezone boundary archive: %w", copyErr)
		}
		return fmt.Errorf("close timezone boundary archive: %w", closeErr)
	}
	return nil
}

// VerifyArchive computes the digest of the exact local ZIP bytes.
func VerifyArchive(path, expectedSHA256 string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open timezone boundary archive: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash timezone boundary archive: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expectedSHA256 {
		return fmt.Errorf("timezone boundary archive SHA256 mismatch: got %s", actual)
	}
	return nil
}

// Import loads and promotes a verified timezone-boundary-builder archive atomically.
func Import(ctx context.Context, pool *pgxpool.Pool, options Options) (Result, error) {
	if pool == nil {
		return Result{}, errors.New("timezone boundary database pool is required")
	}
	if err := options.Validate(); err != nil {
		return Result{}, err
	}
	if err := VerifyArchive(options.ArchivePath, options.SHA256); err != nil {
		return Result{}, err
	}
	var existing Result
	err := pool.QueryRow(ctx, `SELECT id,boundary_count FROM osm_catalog.timezone_datasets
		WHERE provider=$1 AND release=$2 AND source_sha256=$3 AND state='active'`,
		Provider, options.Release, options.SHA256).Scan(&existing.DatasetID, &existing.FeatureCount)
	if err == nil {
		return existing, nil
	}
	if err != pgx.ErrNoRows {
		return Result{}, fmt.Errorf("check active timezone dataset: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin timezone boundary import: %w", err)
	}
	defer tx.Rollback(ctx)

	var result Result
	err = tx.QueryRow(ctx, `
		INSERT INTO osm_catalog.timezone_datasets
			(provider, release, source_url, source_sha256)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider,release,source_sha256) DO UPDATE SET
			source_url=EXCLUDED.source_url,boundary_count=0,state='building',promoted_at=NULL,retired_at=NULL
		WHERE osm_catalog.timezone_datasets.state='retired'
		RETURNING id`, Provider, options.Release, options.SourceURL, options.SHA256).Scan(&result.DatasetID)
	if err != nil {
		return Result{}, fmt.Errorf("create timezone dataset: %w", err)
	}

	result.FeatureCount, err = streamArchive(options.ArchivePath, func(tzid string, geometry json.RawMessage) error {
		var geometryID int64
		if insertErr := tx.QueryRow(ctx, insertGeometrySQL, result.DatasetID, tzid, geometry).Scan(&geometryID); insertErr != nil {
			return fmt.Errorf("insert timezone %q: %w", tzid, insertErr)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if result.FeatureCount == 0 {
		return Result{}, errors.New("timezone boundary archive contains no features")
	}
	if _, err := tx.Exec(ctx, `UPDATE osm_catalog.timezone_datasets SET boundary_count=$2 WHERE id=$1`, result.DatasetID, result.FeatureCount); err != nil {
		return Result{}, fmt.Errorf("record timezone boundary count: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT osm_catalog.promote_timezone_dataset($1)`, result.DatasetID); err != nil {
		return Result{}, fmt.Errorf("promote timezone dataset: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit timezone boundary import: %w", err)
	}
	return result, nil
}

const insertGeometrySQL = `
	WITH parsed AS (
		SELECT ST_Force2D(ST_SetSRID(ST_GeomFromGeoJSON($3), 4326)) AS geom
	), validated AS (
		SELECT ST_Multi(geom)::geometry(MultiPolygon, 4326) AS geom
		FROM parsed
		WHERE GeometryType(geom) IN ('POLYGON', 'MULTIPOLYGON')
		  AND NOT ST_IsEmpty(geom)
		  AND ST_IsValid(geom)
	)
	INSERT INTO osm_catalog.timezone_geometries (dataset_id, tzid, boundary)
	SELECT $1, $2, geom FROM validated
	RETURNING id`

type geoJSONFeature struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

func streamArchive(path string, consume func(string, json.RawMessage) error) (int64, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("open timezone boundary ZIP: %w", err)
	}
	defer archive.Close()

	var geoJSON *zip.File
	var fallback *zip.File
	fallbackCount := 0
	for _, candidate := range archive.File {
		name := strings.ToLower(filepath.Base(candidate.Name))
		if name == "combined.json" || name == "combined.geojson" {
			if geoJSON != nil {
				return 0, errors.New("timezone boundary ZIP contains multiple combined GeoJSON files")
			}
			geoJSON = candidate
			continue
		}
		if strings.HasSuffix(name, ".geojson") || strings.HasSuffix(name, ".json") {
			fallbackCount++
			fallback = candidate
		}
	}
	if geoJSON == nil && fallbackCount == 1 {
		geoJSON = fallback
	}
	if geoJSON == nil {
		return 0, errors.New("timezone boundary ZIP must contain combined.json, combined.geojson, or one GeoJSON file")
	}
	reader, err := geoJSON.Open()
	if err != nil {
		return 0, fmt.Errorf("open timezone boundary GeoJSON: %w", err)
	}
	defer reader.Close()
	return streamFeatureCollection(reader, consume)
}

func streamFeatureCollection(reader io.Reader, consume func(string, json.RawMessage) error) (int64, error) {
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return 0, errors.New("timezone boundary GeoJSON must be an object")
	}
	var count int64
	foundFeatures := false
	collectionType := ""
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return 0, fmt.Errorf("decode timezone boundary GeoJSON: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return 0, errors.New("timezone boundary GeoJSON contains a non-string key")
		}
		if key == "type" {
			if err := decoder.Decode(&collectionType); err != nil {
				return 0, errors.New("timezone boundary GeoJSON type must be a string")
			}
			continue
		}
		if key != "features" {
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return 0, fmt.Errorf("decode timezone boundary GeoJSON field %q: %w", key, err)
			}
			continue
		}
		if foundFeatures {
			return 0, errors.New("timezone boundary GeoJSON contains duplicate features fields")
		}
		foundFeatures = true
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return 0, errors.New("timezone boundary GeoJSON features must be an array")
		}
		for decoder.More() {
			var feature geoJSONFeature
			if err := decoder.Decode(&feature); err != nil {
				return 0, fmt.Errorf("decode timezone boundary feature %d: %w", count+1, err)
			}
			tzid, err := featureTZID(feature)
			if err != nil {
				return 0, fmt.Errorf("timezone boundary feature %d: %w", count+1, err)
			}
			if err := consume(tzid, feature.Geometry); err != nil {
				return 0, err
			}
			count++
		}
		if _, err := decoder.Token(); err != nil {
			return 0, fmt.Errorf("close timezone boundary features: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return 0, fmt.Errorf("close timezone boundary GeoJSON: %w", err)
	}
	if !foundFeatures {
		return 0, errors.New("timezone boundary GeoJSON has no features field")
	}
	if collectionType != "FeatureCollection" {
		return 0, errors.New("timezone boundary GeoJSON type must be FeatureCollection")
	}
	return count, nil
}

func featureTZID(feature geoJSONFeature) (string, error) {
	if feature.Type != "Feature" {
		return "", errors.New("type must be Feature")
	}
	var properties struct {
		TZID string `json:"tzid"`
	}
	if err := json.Unmarshal(feature.Properties, &properties); err != nil {
		return "", errors.New("properties must be an object")
	}
	if !tzidPattern.MatchString(properties.TZID) || len(properties.TZID) > 255 {
		return "", fmt.Errorf("invalid tzid %q", properties.TZID)
	}
	var geometry struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(feature.Geometry, &geometry); err != nil || (geometry.Type != "Polygon" && geometry.Type != "MultiPolygon") {
		return "", errors.New("geometry must be a Polygon or MultiPolygon")
	}
	return properties.TZID, nil
}
