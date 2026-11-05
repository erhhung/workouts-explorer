package api

import (
	"compress/gzip"
	"net/http"
	"strings"
)

const minimumTileGzipBytes = 1024
const maximumTileCompressors = 4

// Cache hits bypass render slots, so encoder memory needs its own process-wide
// bound. Nil slots allocate lazily; returned writers retain no response writer.
var tileCompressors = func() chan *gzip.Writer {
	writers := make(chan *gzip.Writer, maximumTileCompressors)
	for range maximumTileCompressors {
		writers <- nil
	}
	return writers
}()

func acceptsTileGzip(header http.Header) bool {
	qualities := map[string]int{}
	for _, line := range header.Values("Accept-Encoding") {
		for _, token := range strings.Split(line, ",") {
			parts := strings.Split(token, ";")
			coding := strings.ToLower(strings.TrimSpace(parts[0]))
			if coding != "gzip" && coding != "identity" && coding != "*" {
				continue
			}
			quality := tileEncodingQuality(parts[1:])
			// Conflicting duplicates must not override an explicit refusal.
			if previous, ok := qualities[coding]; ok {
				quality = min(quality, previous)
			}
			qualities[coding] = quality
		}
	}
	quality, explicit := qualities["gzip"]
	if !explicit {
		quality = qualities["*"]
	}
	return quality > 0 && quality >= qualities["identity"]
}

func tileEncodingQuality(parameters []string) int {
	if len(parameters) == 0 {
		return 1000
	}
	if len(parameters) != 1 {
		return 0
	}
	name, value, ok := strings.Cut(strings.TrimSpace(parameters[0]), "=")
	value = strings.TrimSpace(value)
	if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") || len(value) == 0 || (value[0] != '0' && value[0] != '1') {
		return 0
	}
	quality := int(value[0]-'0') * 1000
	if len(value) == 1 {
		return quality
	}
	if len(value) > 5 || value[1] != '.' {
		return 0
	}
	scale := 100
	for _, digit := range value[2:] {
		if digit < '0' || digit > '9' || value[0] == '1' && digit != '0' {
			return 0
		}
		quality += int(digit-'0') * scale
		scale /= 10
	}
	return quality
}
