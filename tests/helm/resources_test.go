package helm_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
)

type container struct {
	Name           string
	Image          string
	Resources      map[string]map[string]string
	LivenessProbe  *probe `yaml:"livenessProbe"`
	ReadinessProbe *probe `yaml:"readinessProbe"`
	Ports          []struct {
		Name          string
		ContainerPort int `yaml:"containerPort"`
	}
	Env []struct {
		Name      string
		Value     string
		ValueFrom struct {
			FieldRef struct {
				FieldPath string `yaml:"fieldPath"`
			} `yaml:"fieldRef"`
		} `yaml:"valueFrom"`
	}
	SecurityContext map[string]any `yaml:"securityContext"`
	VolumeMounts    []struct {
		Name      string
		MountPath string `yaml:"mountPath"`
		ReadOnly  bool   `yaml:"readOnly"`
	} `yaml:"volumeMounts"`
}

func TestHelmImageDigestsIncludeHooks(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, pinned := range []bool{false, true} {
		args := []string{"template", "digest-test", chartDir, "--set", "api.bootstrap.enabled=true,api.roleProvisioning.enabled=true,image.registry=example.test,image.tag=fixture"}
		if pinned {
			for _, name := range []string{"workouts-api", "workouts-worker", "workouts-ui"} {
				args = append(args, "--set-string", "image.digests."+name+"=sha256:"+strings.Repeat("a", 64))
			}
		}
		output, err := exec.Command("helm", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("render: %v\n%s", err, output)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(output))
		hooks := 0
		for {
			var item workload
			err := decoder.Decode(&item)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if item.Kind != "Deployment" && item.Kind != "Job" {
				continue
			}
			for _, c := range append(item.Spec.Template.Spec.Containers, item.Spec.Template.Spec.InitContainers...) {
				if !strings.HasPrefix(c.Image, "example.test/workouts-") {
					continue
				}
				want := strings.Split(c.Image, ":fixture")[0] + ":fixture"
				if pinned {
					want += "@sha256:" + strings.Repeat("a", 64)
				}
				if c.Image != want {
					t.Errorf("%s/%s image=%q want %q", item.Kind, c.Name, c.Image, want)
				}
				if item.Kind == "Job" {
					hooks++
				}
			}
		}
		if hooks < 4 {
			t.Fatalf("did not exercise migration/bootstrap/OSM hooks: %d", hooks)
		}
	}
	bad, err := exec.Command("helm", "template", "bad-digest", chartDir, "--set-string", "image.digests.workouts-api=not-a-digest").CombinedOutput()
	if err == nil {
		t.Fatalf("invalid digest passed schema validation: %s", bad)
	}
}

type probe struct {
	FailureThreshold int `yaml:"failureThreshold"`
	TimeoutSeconds   int `yaml:"timeoutSeconds"`
	HTTPGet          struct {
		Port any
	} `yaml:"httpGet"`
}

type workload struct {
	Kind     string
	Metadata struct{ Name string }
	Data     map[string]string
	Spec     struct {
		Template struct {
			Metadata struct {
				Labels      map[string]string
				Annotations map[string]string
			}
			Spec struct {
				Containers                   []container `yaml:"containers"`
				InitContainers               []container `yaml:"initContainers"`
				AutomountServiceAccountToken *bool       `yaml:"automountServiceAccountToken"`
				Volumes                      []struct {
					Name      string
					ConfigMap struct{ Name string } `yaml:"configMap"`
					Secret    struct {
						SecretName  string `yaml:"secretName"`
						DefaultMode int    `yaml:"defaultMode"`
						Items       []struct{ Key, Path string }
					}
				}
			}
		}
	}
}

func TestHelmPodResources(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart rendering tests")
	}
	owners := map[string]string{
		"ui": "ui", "api": "api", "worker": "worker",
		"coverage-worker": "coverageWorker", "tiles": "tileServer",
		"migration": "api", "role-provisioning": "api", "bootstrap-admin": "api",
		"osm-migration": "osm", "timezone-import": "osm",
	}
	for _, overrides := range []bool{false, true} {
		name := "defaults"
		if overrides {
			name = "component overrides"
		}
		t.Run(name, func(t *testing.T) {
			args := []string{"template", "resource-test", chartDir, "--set", "api.bootstrap.enabled=true,api.roleProvisioning.enabled=true"}
			want := make(map[string]map[string]map[string]string)
			for i, component := range []string{"ui", "api", "worker", "coverageWorker", "tileServer", "osm", "otel.collector"} {
				resources := map[string]map[string]string{
					"requests": {"cpu": "25m", "memory": "64Mi"},
					"limits":   {"cpu": "250m", "memory": "256Mi"},
				}
				if overrides {
					resources["requests"]["cpu"] = fmt.Sprintf("%dm", 30+i)
					resources["requests"]["memory"] = fmt.Sprintf("%dMi", 70+i)
					resources["limits"]["cpu"] = fmt.Sprintf("%dm", 300+i)
					resources["limits"]["memory"] = fmt.Sprintf("%dMi", 300+i)
					for group, quantities := range resources {
						for resource, quantity := range quantities {
							args = append(args, "--set-string", component+".resources."+group+"."+resource+"="+quantity)
						}
					}
				}
				want[component] = resources
			}
			cmd := exec.Command("helm", args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			rendered, err := cmd.Output()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, &stderr)
			}
			decoder := yaml.NewDecoder(bytes.NewReader(rendered))
			seen := make(map[string]bool)
			for {
				var item workload
				if err := decoder.Decode(&item); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if item.Kind != "Deployment" && item.Kind != "Job" {
					continue
				}
				component := item.Spec.Template.Metadata.Labels["app.kubernetes.io/component"]
				owner, ok := owners[component]
				if !ok || seen[component] {
					t.Fatalf("unexpected or duplicate workload component %q", component)
				}
				seen[component] = true
				pod := item.Spec.Template.Spec
				initCount := 0
				if component == "bootstrap-admin" {
					initCount = 1
				}
				containerCount := 1
				if component == "ui" || component == "tiles" {
					containerCount++
				}
				if len(pod.Containers) != containerCount || len(pod.InitContainers) != initCount {
					t.Fatalf("%s: unexpected container counts: %d regular, %d init", component, len(pod.Containers), len(pod.InitContainers))
				}
				for _, c := range append(pod.Containers, pod.InitContainers...) {
					resourceOwner := owner
					if c.Name == "metrics-collector" {
						resourceOwner = "otel.collector"
					}
					if !reflect.DeepEqual(c.Resources, want[resourceOwner]) {
						t.Errorf("%s/%s resources = %v, want %s.resources = %v", component, c.Name, c.Resources, resourceOwner, want[resourceOwner])
					}
					for name, p := range map[string]*probe{"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe} {
						if item.Kind == "Deployment" {
							if p == nil || p.FailureThreshold != 6 || p.TimeoutSeconds != 5 {
								t.Errorf("%s/%s %s probe = %+v, want failureThreshold=6, timeoutSeconds=5", component, c.Name, name, p)
							}
						} else if p != nil {
							t.Errorf("%s/%s one-shot Job unexpectedly has a %s probe", component, c.Name, name)
						}
					}
				}
			}
			for component := range owners {
				if !seen[component] {
					t.Errorf("missing workload %s", component)
				}
			}
		})
	}
}

func TestManualOSMJobResources(t *testing.T) {
	for _, name := range []string{"manual-update-job.yaml", "manual-identity-eval-job.yaml"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(repoPath("osm/" + name))
			if err != nil {
				t.Fatal(err)
			}
			var job workload
			if err := yaml.Unmarshal(data, &job); err != nil {
				t.Fatal(err)
			}
			pod := job.Spec.Template.Spec
			if job.Kind != "Job" || len(pod.Containers) != 1 || len(pod.InitContainers) != 0 {
				t.Fatal("expected a single-container manual Job")
			}
			want := map[string]map[string]string{
				"requests": {"cpu": "25m", "memory": "64Mi"},
				"limits":   {"cpu": "250m", "memory": "256Mi"},
			}
			if name == "manual-update-job.yaml" {
				want["requests"]["ephemeral-storage"] = "${OSM_RESOURCE_REQUEST_EPHEMERAL_STORAGE}"
				want["limits"]["ephemeral-storage"] = "${OSM_RESOURCE_LIMIT_EPHEMERAL_STORAGE}"
			}
			if got := pod.Containers[0].Resources; !reflect.DeepEqual(got, want) {
				t.Errorf("resources = %v, want %v", got, want)
			}
		})
	}
}

func TestDevelopmentPostgresProbes(t *testing.T) {
	data, err := os.ReadFile(repoPath("deploy/dev/postgres.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var item workload
		if err := decoder.Decode(&item); err == io.EOF {
			t.Fatal("development PostgreSQL StatefulSet is missing")
		} else if err != nil {
			t.Fatal(err)
		}
		if item.Kind != "StatefulSet" {
			continue
		}
		if len(item.Spec.Template.Spec.Containers) != 1 {
			t.Fatal("expected a single PostgreSQL container")
		}
		c := item.Spec.Template.Spec.Containers[0]
		for name, p := range map[string]*probe{"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe} {
			wantFailureThreshold := 6
			if name == "liveness" {
				wantFailureThreshold = 4
			}
			if p == nil || p.FailureThreshold != wantFailureThreshold || p.TimeoutSeconds != 5 {
				t.Errorf("PostgreSQL %s probe = %+v, want failureThreshold=%d, timeoutSeconds=5", name, p, wantFailureThreshold)
			}
		}
		return
	}
}
