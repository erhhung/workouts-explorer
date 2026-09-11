package timezone

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validFeatureCollection = `{
  "type":"FeatureCollection",
  "features":[
    {"type":"Feature","properties":{"tzid":"America/Los_Angeles"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]] }},
    {"type":"Feature","properties":{"tzid":"Etc/GMT+8"},"geometry":{"type":"MultiPolygon","coordinates":[]}}
  ]
}`

func TestOptionsValidate(t *testing.T) {
	valid := Options{
		ArchivePath: "/tmp/timezones.zip",
		Release:     "2026a",
		SourceURL:   "https://github.com/evansiroky/timezone-boundary-builder/releases/download/2026a/timezones.geojson.zip",
		SHA256:      strings.Repeat("a", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{"missing archive", func(options *Options) { options.ArchivePath = "" }},
		{"blank release", func(options *Options) { options.Release = " " }},
		{"non-HTTPS source", func(options *Options) { options.SourceURL = "http://example.com/archive.zip" }},
		{"uppercase digest", func(options *Options) { options.SHA256 = strings.Repeat("A", 64) }},
		{"short digest", func(options *Options) { options.SHA256 = "abc" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			if err := options.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestVerifyArchiveRequiresExactDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timezones.zip")
	content := []byte("archive bytes")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if err := VerifyArchive(path, hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("verify archive: %v", err)
	}
	if err := VerifyArchive(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestStreamFeatureCollection(t *testing.T) {
	var tzids []string
	count, err := streamFeatureCollection(strings.NewReader(validFeatureCollection), func(tzid string, geometry json.RawMessage) error {
		tzids = append(tzids, tzid)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || strings.Join(tzids, ",") != "America/Los_Angeles,Etc/GMT+8" {
		t.Fatalf("unexpected streamed features: count=%d tzids=%v", count, tzids)
	}
}

func TestStreamFeatureCollectionRejectsInvalidFeature(t *testing.T) {
	input := strings.Replace(validFeatureCollection, "America/Los_Angeles", "Not_A_Timezone", 1)
	if _, err := streamFeatureCollection(strings.NewReader(input), func(string, json.RawMessage) error { return nil }); err == nil {
		t.Fatal("expected invalid tzid error")
	}
}

func TestStreamArchiveAcceptsSingleGeoJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timezones.zip")
	var content bytes.Buffer
	archive := zip.NewWriter(&content)
	entry, err := archive.Create("dist/timezones.geojson")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(validFeatureCollection)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	count, err := streamArchive(path, func(string, json.RawMessage) error { return nil })
	if err != nil || count != 2 {
		t.Fatalf("stream archive: count=%d err=%v", count, err)
	}
}
