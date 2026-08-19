package osm

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestFetchGeofabrikCatalog(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != GeofabrikIndexURL || request.Header.Get("User-Agent") == "" {
			t.Fatal("catalog request was not constrained")
		}
		body := `{"features":[{"properties":{"id":"norcal","name":"Northern California","urls":{"pbf":"https://download.geofabrik.de/norcal-latest.osm.pbf"}},"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: ioNopCloser{strings.NewReader(body)}, Header: make(http.Header)}, nil
	})}
	regions, err := FetchGeofabrikCatalog(context.Background(), client)
	if err != nil || len(regions) != 1 || regions[0].ID != "geofabrik:norcal" {
		t.Fatalf("regions=%+v err=%v", regions, err)
	}
	if _, found := ResolveRegion(regions, "geofabrik:norcal"); !found {
		t.Fatal("configured region did not resolve")
	}
}

func TestDownloadRegionEnforcesStreamLimit(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: ioNopCloser{strings.NewReader("123456")}, Header: make(http.Header)}, nil
	})}
	destination := filepath.Join(t.TempDir(), "region.osm.pbf")
	_, err := DownloadRegion(context.Background(), client, Region{ID: "geofabrik:test", SourceURL: "https://download.geofabrik.de/test.osm.pbf"}, destination, 5)
	if !errors.Is(err, ErrDownloadTooLarge) {
		t.Fatalf("oversized download error=%v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("oversized download was published")
	}
}

type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }
