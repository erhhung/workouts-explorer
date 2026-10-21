package helm_test

import (
	"bytes"
	"io"
	"os/exec"
	"strconv"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/oasdiff/yaml3"
)

func TestHelmCoverageRetryAndBatchConfiguration(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, test := range []struct{ retry, batch int }{{1, 25}, {0, 1}, {20, 1000}} {
		t.Run(strconv.Itoa(test.retry)+"/"+strconv.Itoa(test.batch), func(t *testing.T) {
			args := []string{"template", "retry-config-test", chartDir}
			if test.retry != 1 {
				args = append(args, "--set", "coverageWorker.autoRetryCount="+strconv.Itoa(test.retry),
					"--set", "worker.coverageBatchSize="+strconv.Itoa(test.batch))
			}
			output, err := exec.Command("helm", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("render: %v\n%s", err, output)
			}
			want := map[string]struct{ container, value string }{
				"COVERAGE_WORKER_AUTO_RETRY_COUNT": {"coverage-worker", strconv.Itoa(test.retry)},
				"WORKER_COVERAGE_BATCH_SIZE":       {"worker", strconv.Itoa(test.batch)},
			}
			seen := map[string]int{}
			decoder := yaml.NewDecoder(bytes.NewReader(output))
			for {
				var item workload
				if err := decoder.Decode(&item); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				for _, container := range item.Spec.Template.Spec.Containers {
					for _, env := range container.Env {
						if expected, ok := want[env.Name]; ok {
							seen[env.Name]++
							if item.Kind != "Deployment" || container.Name != expected.container || env.Value != expected.value || env.ValueFrom.SecretKeyRef.Name != "" {
								t.Errorf("unexpected env placement/value: container=%s env=%+v", container.Name, env)
							}
							t.Setenv(env.Name, env.Value)
						}
					}
				}
			}
			for key := range want {
				if seen[key] != 1 {
					t.Errorf("%s appears %d times, want exactly one", key, seen[key])
				}
			}
			t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
			t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
			cfg, err := config.LoadCoverageWorker()
			if err != nil || cfg.AutoRetryCount != test.retry {
				t.Fatalf("rendered runtime retry policy=%d err=%v", cfg.AutoRetryCount, err)
			}
		})
	}
	for _, setting := range []string{"coverageWorker.autoRetryCount=-1", "coverageWorker.autoRetryCount=21", "coverageWorker.autoRetryCount=1.5",
		"worker.coverageBatchSize=0", "worker.coverageBatchSize=1001", "worker.coverageBatchSize=1.5"} {
		t.Run("invalid/"+setting, func(t *testing.T) {
			if output, err := exec.Command("helm", "template", "invalid-retry-test", chartDir, "--set", setting).CombinedOutput(); err == nil {
				t.Fatalf("invalid setting rendered: %s\n%s", setting, output)
			}
		})
	}
}
