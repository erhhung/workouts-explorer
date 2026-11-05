package config

import (
	"testing"
	"time"
)

func TestMapTilesConfiguration(t *testing.T) {
	names := []string{"MAP_TILE_CONCURRENCY", "MAP_TILE_QUEUE_SIZE", "MAP_TILE_CACHE_BYTES", "MAP_TILE_CACHE_ENTRIES",
		"MAP_TILE_CACHE_TTL", "MAP_TILE_QUEUE_TIMEOUT", "MAP_TILE_REQUEST_TIMEOUT"}
	for _, name := range names {
		t.Setenv(name, "")
	}
	defaults := (MapTiles{}).WithDefaults()
	if got, err := loadMapTiles(); err != nil || got != defaults {
		t.Fatalf("defaults=%+v err=%v", got, err)
	}
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"MAP_TILE_CONCURRENCY", "1", true}, {"MAP_TILE_CONCURRENCY", "16", true},
		{"MAP_TILE_CONCURRENCY", "0", false}, {"MAP_TILE_CONCURRENCY", "17", false},
		{"MAP_TILE_QUEUE_SIZE", "0", true}, {"MAP_TILE_QUEUE_SIZE", "256", true}, {"MAP_TILE_QUEUE_SIZE", "257", false},
		{"MAP_TILE_CACHE_BYTES", "0", true}, {"MAP_TILE_CACHE_BYTES", "134217728", true}, {"MAP_TILE_CACHE_BYTES", "134217729", false},
		{"MAP_TILE_CACHE_ENTRIES", "0", false}, {"MAP_TILE_CACHE_ENTRIES", "4096", true}, {"MAP_TILE_CACHE_ENTRIES", "4097", false},
		{"MAP_TILE_CACHE_TTL", "1s", true}, {"MAP_TILE_CACHE_TTL", "5m", true}, {"MAP_TILE_CACHE_TTL", "301s", false},
		{"MAP_TILE_QUEUE_TIMEOUT", "100ms", true}, {"MAP_TILE_QUEUE_TIMEOUT", "3s", true}, {"MAP_TILE_QUEUE_TIMEOUT", "99ms", false},
		{"MAP_TILE_REQUEST_TIMEOUT", "5s", true}, {"MAP_TILE_REQUEST_TIMEOUT", "15s", true}, {"MAP_TILE_REQUEST_TIMEOUT", "16s", false},
		{"MAP_TILE_REQUEST_TIMEOUT", "canary-not-a-duration", false},
	} {
		t.Run(tc.name+"/"+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			got, err := loadMapTiles()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
			if err == nil && got.WithDefaults() != got {
				t.Fatal("explicit settings were overwritten")
			}
		})
	}
	defaults.QueueSize, defaults.CacheBytes = 0, 0
	if defaults.WithDefaults() != defaults {
		t.Fatal("explicit zero settings must survive defaults")
	}
	defaults.QueueTimeout = defaults.RequestTimeout
	if defaults.Validate() == nil {
		t.Fatal("equal queue/request deadlines accepted")
	}
	defaults.QueueTimeout = time.Second
	if defaults.Validate() != nil {
		t.Fatal("valid explicit zero settings rejected")
	}
}
