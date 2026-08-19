package osm

import (
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
	"strconv"
	"strings"
)

const GeofabrikIndexURL = "https://download.geofabrik.de/index-v1.json"

var ErrDownloadTooLarge = errors.New("OSM region download exceeds configured byte limit")

type Region struct {
	ID              string
	Provider        string
	ProviderID      string
	Name            string
	CatalogURL      string
	SourceURL       string
	AdvertisedBytes int64
	GeometryJSON    []byte
}

type geofabrikIndex struct {
	Features []struct {
		Properties struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			URLs struct {
				PBF string `json:"pbf"`
			} `json:"urls"`
		} `json:"properties"`
		Geometry json.RawMessage `json:"geometry"`
	} `json:"features"`
}

func FetchGeofabrikCatalog(ctx context.Context, client *http.Client) ([]Region, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, GeofabrikIndexURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "workouts-explorer-osm/1")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download Geofabrik catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download Geofabrik catalog: unexpected HTTP status %d", response.StatusCode)
	}
	var index geofabrikIndex
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	if err := decoder.Decode(&index); err != nil {
		return nil, fmt.Errorf("decode Geofabrik catalog: %w", err)
	}
	regions := make([]Region, 0, len(index.Features))
	for _, feature := range index.Features {
		if !validRegionPart(feature.Properties.ID) || feature.Properties.Name == "" || !validGeofabrikURL(feature.Properties.URLs.PBF) {
			continue
		}
		var geometry struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(feature.Geometry, &geometry); err != nil || (geometry.Type != "Polygon" && geometry.Type != "MultiPolygon") {
			continue
		}
		regions = append(regions, Region{
			ID: "geofabrik:" + feature.Properties.ID, Provider: "geofabrik", ProviderID: feature.Properties.ID,
			Name: feature.Properties.Name, CatalogURL: GeofabrikIndexURL, SourceURL: feature.Properties.URLs.PBF,
			GeometryJSON: append([]byte(nil), feature.Geometry...),
		})
	}
	if len(regions) == 0 {
		return nil, errors.New("Geofabrik catalog contains no usable regions")
	}
	return regions, nil
}

func ResolveRegion(regions []Region, configuredID string) (Region, bool) {
	for _, region := range regions {
		if region.ID == configuredID {
			return region, true
		}
	}
	return Region{}, false
}

type DownloadResult struct {
	Bytes  int64
	SHA256 string
}

func DownloadRegion(ctx context.Context, client *http.Client, region Region, destination string, maximumBytes int64) (DownloadResult, error) {
	if maximumBytes < 1 || !validGeofabrikURL(region.SourceURL) {
		return DownloadResult{}, errors.New("invalid OSM region download configuration")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, region.SourceURL, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	request.Header.Set("User-Agent", "workouts-explorer-osm/1")
	response, err := client.Do(request)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("download OSM region %s: %w", region.ID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("download OSM region %s: unexpected HTTP status %d", region.ID, response.StatusCode)
	}
	if length := response.Header.Get("Content-Length"); length != "" {
		advertised, parseErr := strconv.ParseInt(length, 10, 64)
		if parseErr != nil || advertised < 0 {
			return DownloadResult{}, fmt.Errorf("download OSM region %s: invalid content length", region.ID)
		}
		if advertised > maximumBytes {
			return DownloadResult{}, ErrDownloadTooLarge
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return DownloadResult{}, fmt.Errorf("prepare OSM download: %w", err)
	}
	temporary := destination + ".partial"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("create OSM download: %w", err)
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maximumBytes+1))
	if err != nil {
		return DownloadResult{}, fmt.Errorf("write OSM download: %w", err)
	}
	if written > maximumBytes {
		return DownloadResult{}, ErrDownloadTooLarge
	}
	if err := file.Sync(); err != nil {
		return DownloadResult{}, fmt.Errorf("sync OSM download: %w", err)
	}
	if err := file.Close(); err != nil {
		return DownloadResult{}, fmt.Errorf("close OSM download: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return DownloadResult{}, fmt.Errorf("publish OSM download: %w", err)
	}
	keep = true
	return DownloadResult{Bytes: written, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func validGeofabrikURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "download.geofabrik.de" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && strings.HasSuffix(parsed.Path, ".osm.pbf")
}

func validRegionPart(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}
