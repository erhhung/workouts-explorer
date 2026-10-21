package helm_test

import (
	"bytes"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/oasdiff/yaml3"
)

func TestHelmCoverageWorkerTimeouts(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, test := range []struct {
		name, timeout, lease string
	}{
		{"defaults", "360s", "420s"},
		{"custom initial", "5s", "420s"},
		{"maximum initial", "10m", "11m"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"template", "coverage-timeouts-test", chartDir}
			if test.name != "defaults" {
				args = append(args, "--set-string", "coverageWorker.routeTimeout="+test.timeout,
					"--set-string", "coverageWorker.leaseDuration="+test.lease)
			}
			output, err := exec.Command("helm", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("render: %v\n%s", err, output)
			}
			want := map[string]string{
				"COVERAGE_WORKER_ROUTE_TIMEOUT":      test.timeout,
				"COVERAGE_WORKER_LEASE_DURATION":     test.lease,
				"COVERAGE_WORKER_HEARTBEAT_INTERVAL": "20s",
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
				for _, c := range item.Spec.Template.Spec.Containers {
					for _, env := range c.Env {
						if value, ok := want[env.Name]; ok {
							seen[env.Name]++
							if item.Kind != "Deployment" || c.Name != "coverage-worker" || env.Value != value || env.ValueFrom.SecretKeyRef.Name != "" {
								t.Errorf("unexpected timing env: %+v", env)
							}
							// Load public rendered timings without making database connections.
							t.Setenv(env.Name, env.Value)
						}
					}
				}
			}
			for name := range want {
				if seen[name] != 1 {
					t.Errorf("expected exactly one %s, got %d", name, seen[name])
				}
			}
			t.Setenv("COVERAGE_WORKER_DATABASE_URL", "postgresql://coverage.invalid/workouts")
			t.Setenv("COVERAGE_WORKER_OSM_DATABASE_URL", "postgresql://coverage.invalid/osm")
			cfg, err := config.LoadCoverageWorker()
			if err != nil {
				t.Fatal(err)
			}
			if timeout, _ := time.ParseDuration(test.timeout); cfg.RouteTimeout != timeout {
				t.Errorf("loaded initial timeout=%s, want %s", cfg.RouteTimeout, timeout)
			}
			if lease, _ := time.ParseDuration(test.lease); cfg.LeaseDuration != lease {
				t.Errorf("loaded lease TTL=%s, want %s", cfg.LeaseDuration, lease)
			}
			if cfg.HeartbeatInterval != 20*time.Second || cfg.RouteTimeout+10*time.Second >= cfg.LeaseDuration {
				t.Error("rendered timing does not preserve heartbeat and cleanup margin")
			}
		})
	}
}
