package osm

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

func TestDownloadRegionReportsSourceURLAndHTTPStatus(t *testing.T) {
	source := "https://download.geofabrik.de/europe/norway/svalbard-janmayen/svalbard-janmayen-latest.osm.pbf"
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: ioNopCloser{strings.NewReader("")}, Header: make(http.Header)}, nil
			})}
			destination := filepath.Join(t.TempDir(), "region.osm.pbf")
			_, err := DownloadRegion(context.Background(), client, Region{ID: "geofabrik:svalbard-janmayen", SourceURL: source}, destination, 1024)
			want := "Failed to download " + source + " | unexpected HTTP status " + strconv.Itoa(status)
			if err == nil || err.Error() != want {
				t.Fatalf("error=%v, want %q", err, want)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("failed HTTP response published a PBF", err)
			}
		})
	}
}

func TestDownloadRegionPreservesTransportCauseWithoutRepeatingURL(t *testing.T) {
	source := "https://download.geofabrik.de/europe/monaco/monaco-latest.osm.pbf"
	cause := errors.New("upstream connection reset")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, cause })}
	_, err := DownloadRegion(context.Background(), client, Region{ID: "geofabrik:monaco", SourceURL: source}, filepath.Join(t.TempDir(), "region.osm.pbf"), 1024)
	if !errors.Is(err, cause) || err.Error() != "Failed to download "+source+" | "+cause.Error() {
		t.Fatal("transport error lost its identity or duplicated context", err)
	}
}

type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }
