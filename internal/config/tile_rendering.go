package config

import (
	"fmt"
	"time"
)

type MapTiles struct {
	// Concurrency is a ceiling; the API's fixed body-lifetime budget may impose a lower limit.
	Concurrency    int
	QueueSize      int
	CacheBytes     int
	CacheEntries   int
	CacheTTL       time.Duration
	QueueTimeout   time.Duration
	RequestTimeout time.Duration
}

// Only a completely omitted configuration receives defaults. Zero queue size
// and zero cache bytes are intentional settings, not missing values.
func (c MapTiles) WithDefaults() MapTiles {
	if c == (MapTiles{}) {
		return MapTiles{Concurrency: 4, QueueSize: 64, CacheBytes: 32 << 20, CacheEntries: 512,
			CacheTTL: 30 * time.Second, QueueTimeout: time.Second, RequestTimeout: 6 * time.Second}
	}
	return c
}

func (c MapTiles) Validate() error {
	if c.Concurrency < 1 || c.Concurrency > 16 || c.QueueSize < 0 || c.QueueSize > 256 ||
		c.CacheBytes < 0 || c.CacheBytes > 128<<20 || c.CacheEntries < 1 || c.CacheEntries > 4096 ||
		c.CacheTTL < time.Second || c.CacheTTL > 5*time.Minute ||
		c.QueueTimeout < 100*time.Millisecond || c.QueueTimeout > 3*time.Second ||
		c.RequestTimeout < 5*time.Second || c.RequestTimeout > 15*time.Second {
		return fmt.Errorf("map tile configuration is outside the supported bounds")
	}
	if c.QueueTimeout >= c.RequestTimeout {
		return fmt.Errorf("MAP_TILE_QUEUE_TIMEOUT must be less than MAP_TILE_REQUEST_TIMEOUT")
	}
	return nil
}

func loadMapTiles() (MapTiles, error) {
	c := (MapTiles{}).WithDefaults()
	var err error
	for _, field := range []struct {
		name string
		dest *int
		min  int
		max  int
	}{
		{"MAP_TILE_CONCURRENCY", &c.Concurrency, 1, 16},
		{"MAP_TILE_QUEUE_SIZE", &c.QueueSize, 0, 256},
		{"MAP_TILE_CACHE_BYTES", &c.CacheBytes, 0, 128 << 20},
		{"MAP_TILE_CACHE_ENTRIES", &c.CacheEntries, 1, 4096},
	} {
		*field.dest, err = integerRange(field.name, *field.dest, field.min, field.max)
		if err != nil {
			return MapTiles{}, err
		}
	}
	for _, field := range []struct {
		name string
		dest *time.Duration
		min  time.Duration
		max  time.Duration
	}{
		{"MAP_TILE_CACHE_TTL", &c.CacheTTL, time.Second, 5 * time.Minute},
		{"MAP_TILE_QUEUE_TIMEOUT", &c.QueueTimeout, 100 * time.Millisecond, 3 * time.Second},
		{"MAP_TILE_REQUEST_TIMEOUT", &c.RequestTimeout, 5 * time.Second, 15 * time.Second},
	} {
		*field.dest, err = durationRange(field.name, *field.dest, field.min, field.max)
		if err != nil {
			return MapTiles{}, err
		}
	}
	return c, c.Validate()
}
