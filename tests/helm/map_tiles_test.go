package helm_test

import (
	"bytes"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/oasdiff/yaml3"
)

func TestHelmMapTiles(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	var defaultChecksum string
	for _, test := range []struct {
		name                string
		want                config.MapTiles
		ttl, queue, request string
	}{
		{"defaults", (config.MapTiles{}).WithDefaults(), "30s", "1s", "6s"},
		{"zeros and minimums", config.MapTiles{Concurrency: 1, QueueSize: 0, CacheBytes: 0, CacheEntries: 1,
			CacheTTL: time.Second, QueueTimeout: 100 * time.Millisecond, RequestTimeout: 5 * time.Second}, "1s", "100ms", "5s"},
		{"maximums", config.MapTiles{Concurrency: 16, QueueSize: 256, CacheBytes: 134217728, CacheEntries: 4096,
			CacheTTL: 5 * time.Minute, QueueTimeout: 3 * time.Second, RequestTimeout: 15 * time.Second}, "5m", "3000ms", "15s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{
				"concurrency": strconv.Itoa(test.want.Concurrency), "queueSize": strconv.Itoa(test.want.QueueSize),
				"cacheBytes": strconv.Itoa(test.want.CacheBytes), "cacheEntries": strconv.Itoa(test.want.CacheEntries),
				"cacheTtl": test.ttl, "queueTimeout": test.queue, "requestTimeout": test.request,
			}
			envNames := map[string]string{
				"concurrency": "MAP_TILE_CONCURRENCY", "queueSize": "MAP_TILE_QUEUE_SIZE", "cacheBytes": "MAP_TILE_CACHE_BYTES",
				"cacheEntries": "MAP_TILE_CACHE_ENTRIES", "cacheTtl": "MAP_TILE_CACHE_TTL",
				"queueTimeout": "MAP_TILE_QUEUE_TIMEOUT", "requestTimeout": "MAP_TILE_REQUEST_TIMEOUT",
			}
			args := []string{"template", "tile-config-test", chartDir}
			if test.name != "defaults" {
				for key, value := range values {
					flag := "--set"
					if key == "cacheTtl" || key == "queueTimeout" || key == "requestTimeout" {
						flag = "--set-string"
					}
					args = append(args, flag, "api.mapTiles."+key+"="+value)
				}
			}
			output, err := exec.Command("helm", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("render: %v\n%s", err, output)
			}
			seen := map[string]int{}
			var checksum string
			var apiConfig, martinConfig bool
			decoder := yaml.NewDecoder(bytes.NewReader(output))
			for {
				var item workload
				if err := decoder.Decode(&item); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if item.Kind == "ConfigMap" && item.Metadata.Name == "workouts-explorer-api" {
					apiConfig = true
					for key, value := range item.Data {
						t.Setenv(key, value)
					}
				}
				for key, env := range envNames {
					if value, ok := item.Data[env]; ok {
						seen[env]++
						if item.Kind != "ConfigMap" || item.Metadata.Name != "workouts-explorer-api" || value != values[key] {
							t.Errorf("unexpected %s placement/value: %s %s %q", env, item.Kind, item.Metadata.Name, value)
						}
					}
				}
				if item.Kind == "ConfigMap" && item.Metadata.Name == "workouts-explorer-tiles" {
					martinConfig = true
					if !strings.Contains(item.Data["config.yaml"], "pool_size: 4\n") || !strings.Contains(item.Data["config.yaml"], "cache: disable\n") {
						t.Error("tile admission settings changed Martin pool or cache")
					}
				}
				if item.Kind == "Deployment" && item.Spec.Template.Metadata.Labels["app.kubernetes.io/component"] == "api" {
					checksum = item.Spec.Template.Metadata.Annotations["checksum/config"]
				}
			}
			for _, env := range envNames {
				if seen[env] != 1 {
					t.Errorf("%s appears %d times, want once in private API config", env, seen[env])
				}
			}
			if !apiConfig || !martinConfig || checksum == "" {
				t.Fatal("missing API/Martin config or API rollout checksum")
			}
			if test.name == "defaults" {
				defaultChecksum = checksum
			} else if checksum == defaultChecksum {
				t.Error("tile settings did not change API rollout checksum")
			}
			t.Setenv("API_DATABASE_URL", "postgresql://api.invalid/workouts")
			t.Setenv("RATE_LIMIT_KEY", strings.Repeat("A", 43))
			cfg, err := config.LoadAPI()
			if err != nil || cfg.MapTiles != test.want {
				t.Fatalf("rendered runtime tile config=%+v want %+v err=%v", cfg.MapTiles, test.want, err)
			}
		})
	}
}
