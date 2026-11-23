package helm_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"strconv"
	"testing"

	"github.com/oasdiff/yaml3"
)

func TestOSMJobRetention(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, ttl := range []int{21600, 0, 3600} {
		args := []string{"template", "retention-test", chartDir, "--set", "osm.regionUpdates.enabled=true"}
		if ttl != 21600 {
			args = append(args, "--set", "osm.regionUpdates.ttlSecondsAfterFinished="+strconv.Itoa(ttl))
		}
		raw, err := exec.Command("helm", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("render: %v\n%s", err, raw)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		found := false
		for {
			var item struct {
				Kind string
				Data map[string]string
			}
			if err := decoder.Decode(&item); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if item.Kind != "ConfigMap" || item.Data["job-template.json"] == "" {
				continue
			}
			var job struct {
				Spec struct{ TTLSecondsAfterFinished *int }
			}
			if err := json.Unmarshal([]byte(item.Data["job-template.json"]), &job); err != nil || job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != ttl {
				t.Fatalf("ttl=%d template=%s: %v", ttl, item.Data["job-template.json"], err)
			}
			found = true
		}
		if !found {
			t.Fatal("updater Job template missing")
		}
	}
	for _, ttl := range []string{"-1", "1.5", "2147483648", "6h"} {
		if raw, err := exec.Command("helm", "template", "invalid-retention", chartDir, "--set", "osm.regionUpdates.ttlSecondsAfterFinished="+ttl).CombinedOutput(); err == nil {
			t.Fatalf("invalid TTL rendered: %s\n%s", ttl, raw)
		}
	}
}
