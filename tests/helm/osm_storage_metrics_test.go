package helm_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
)

func TestOptionalOSMStorageMetricsWiring(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			args := []string{"template", "metrics-test", chartDir, "--set", "osm.regionUpdates.enabled=true", "--set", "osm.regionUpdates.storageMetrics.enabled=" + enabled,
				"--set-string", "osm.regionUpdates.storageMetrics.prometheusURL=https://metrics.example.test:9443", "--set-string", "osm.regionUpdates.storageMetrics.prometheusServiceIP=10.43.1.2",
				"--set-string", "osm.regionUpdates.storageMetrics.availableMetric=node_filesystem_avail_bytes", "--set-string", "osm.regionUpdates.storageMetrics.capacityMetric=node_filesystem_size_bytes",
				"--set-json", `osm.regionUpdates.storageMetrics.selectorLabels={"instance":"standalone:9100","mountpoint":"/srv/postgresql"}`}
			raw, err := exec.Command("helm", args...).CombinedOutput()
			if err != nil {
				t.Fatal(err, string(raw))
			}
			decoder := yaml.NewDecoder(bytes.NewReader(raw))
			found := false
			for {
				var object struct {
					Kind string
					Data map[string]string
				}
				if err := decoder.Decode(&object); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if object.Data["job-template.json"] == "" {
					continue
				}
				found = true
				var job struct {
					Spec struct {
						Template struct {
							Spec struct {
								HostAliases []struct {
									IP        string
									Hostnames []string
								}
								Containers []struct {
									Env []struct{ Name, Value string }
								}
							}
						}
					}
				}
				if err = json.Unmarshal([]byte(object.Data["job-template.json"]), &job); err != nil {
					t.Fatal(err)
				}
				env := map[string]string{}
				for _, item := range job.Spec.Template.Spec.Containers[0].Env {
					env[item.Name] = item.Value
				}
				if env["OSM_STORAGE_METRICS_ENABLED"] != enabled || env["OSM_STORAGE_AVAILABLE_METRIC"] != "node_filesystem_avail_bytes" || env["OSM_STORAGE_CAPACITY_METRIC"] != "node_filesystem_size_bytes" || !strings.Contains(env["OSM_STORAGE_SELECTOR_LABELS_JSON"], "standalone:9100") {
					t.Fatal("metrics values not forwarded", env)
				}
				var labels map[string]string
				if json.Unmarshal([]byte(env["OSM_STORAGE_SELECTOR_LABELS_JSON"]), &labels) != nil || len(labels) != 2 || labels["instance"] != "standalone:9100" || labels["mountpoint"] != "/srv/postgresql" {
					t.Fatal("custom selectors inherited deployment-specific labels", labels)
				}
				aliases := job.Spec.Template.Spec.HostAliases
				if enabled == "false" && len(aliases) != 0 {
					t.Fatal("disabled metrics installed a DNS override")
				}
				if enabled == "true" && (len(aliases) != 1 || aliases[0].Hostnames[0] != "metrics.example.test") {
					t.Fatal("DNS override ignored URL hostname")
				}
			}
			if !found {
				t.Fatal("updater template missing")
			}
		})
	}
	if raw, err := exec.Command("helm", "template", "invalid-metrics", chartDir, "--set", "osm.regionUpdates.storageMetrics.enabled=true", "--set-string", "osm.regionUpdates.storageMetrics.prometheusURL=").CombinedOutput(); err == nil {
		t.Fatal("enabled metrics accepted an empty URL", string(raw))
	}
}

func TestRegionUpdatesDefaultOnAndExplicitDisable(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		args := []string{"template", "region-feature-test", chartDir}
		if !enabled {
			args = append(args, "--set", "osm.regionUpdates.enabled=false")
		}
		raw, err := exec.Command("helm", args...).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(raw))
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		controller := false
		apiFlag := ""
		for {
			var item struct {
				Kind     string
				Metadata struct{ Name string }
				Spec     struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Name string
								Env  []struct{ Name, Value string }
							}
						}
					}
				}
			}
			if err := decoder.Decode(&item); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if item.Kind != "Deployment" {
				continue
			}
			if strings.HasSuffix(item.Metadata.Name, "-osm-controller") {
				controller = true
			}
			for _, container := range item.Spec.Template.Spec.Containers {
				if container.Name == "api" {
					for _, entry := range container.Env {
						if entry.Name == "OSM_REGION_UPDATES_ENABLED" {
							apiFlag = entry.Value
						}
					}
				}
			}
		}
		want := "false"
		if enabled {
			want = "true"
		}
		if controller != enabled || apiFlag != want {
			t.Fatalf("enabled=%t controller=%t API=%q", enabled, controller, apiFlag)
		}
	}
}
