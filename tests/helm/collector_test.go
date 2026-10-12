package helm_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
)

func renderCollectorChart(t *testing.T, settings ...string) map[string]workload {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	args := []string{"template", "collector-test", chartDir}
	for _, setting := range settings {
		flag := "--set-string"
		if strings.HasSuffix(setting, ".enabled=false") {
			flag = "--set"
		}
		args = append(args, flag, setting)
	}
	output, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, output)
	}
	items := make(map[string]workload)
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	for {
		var item workload
		if err := decoder.Decode(&item); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		items[item.Kind+"/"+item.Metadata.Name] = item
	}
	return items
}

func TestCollectorSidecars(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings []string
		enabled  bool
	}{
		{"defaults", nil, true},
		{"empty endpoint", []string{"otel.endpoint="}, false},
		{"disabled collector", []string{"otel.collector.enabled=false"}, false},
		{"gRPC override", []string{"otel.endpoint=grpcs://gateway.example.test:4317"}, true},
		{"HTTP disabled collector", []string{"otel.endpoint=https://gateway.example.test:4318", "otel.collector.enabled=false"}, false},
		{"HTTP custom CA", []string{"otel.endpoint=https://gateway.example.test:4318", "otel.trustedCASecret.name=telemetry-ca", "otel.trustedCASecret.key=trust.pem"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, enabled := tc.settings, tc.enabled
			items := renderCollectorChart(t, settings...)
			count := 0
			for key, item := range items {
				if item.Kind != "Deployment" {
					continue
				}
				component := item.Spec.Template.Metadata.Labels["app.kubernetes.io/component"]
				pod := item.Spec.Template.Spec
				want := enabled && (component == "ui" || component == "tiles")
				containerCount := 1
				if want {
					containerCount++
				}
				if len(pod.Containers) != containerCount {
					t.Fatalf("%s: unexpected containers: %+v", key, pod.Containers)
				}
				if !want {
					if item.Spec.Template.Metadata.Annotations["checksum/collector-config"] != "" {
						t.Errorf("%s: unexpected collector rollout checksum", key)
					}
					for _, volume := range pod.Volumes {
						if volume.Name == "collector-config" || volume.Name == "collector-ca" {
							t.Errorf("%s: disabled collector volume still present: %s", key, volume.Name)
						}
					}
					for _, mount := range pod.Containers[0].VolumeMounts {
						if mount.Name == "collector-config" || mount.Name == "collector-ca" {
							t.Errorf("%s: disabled collector mount still present: %s", key, mount.Name)
						}
					}
					continue
				}
				count++
				if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
					t.Errorf("%s: service account token must not be mounted", key)
				}
				if item.Spec.Template.Metadata.Annotations["checksum/collector-config"] == "" {
					t.Errorf("%s: missing collector rollout checksum", key)
				}
				c := pod.Containers[1]
				if len(c.Ports) != 1 || c.Ports[0].Name != "otel-health" || len(c.Ports[0].Name) > 15 || c.Ports[0].ContainerPort != 13133 {
					t.Errorf("%s: invalid collector health port: %+v", key, c.Ports)
				}
				for _, p := range []*probe{c.LivenessProbe, c.ReadinessProbe} {
					if p == nil || p.HTTPGet.Port != "otel-health" {
						t.Errorf("%s: collector probe must target its health port: %+v", key, p)
					}
				}
				if c.Name != "metrics-collector" || c.Image != "docker.io/otel/opentelemetry-collector-contrib:0.156.0@sha256:125bdbeb7590cc1952c5b3430ecf14063568980c2c93d5b38676cc0446ed8108" {
					t.Errorf("%s: wrong collector: %+v", key, c)
				}
				for _, field := range []string{"runAsUser", "runAsGroup"} {
					if c.SecurityContext[field] != 65532 {
						t.Errorf("%s: collector %s=%v", key, field, c.SecurityContext[field])
					}
				}
				if c.SecurityContext["readOnlyRootFilesystem"] != true || c.SecurityContext["allowPrivilegeEscalation"] != false {
					t.Errorf("%s: collector filesystem/privilege restrictions missing", key)
				}
				if len(c.Env) != 2 || c.Env[0].Name != "POD_UID" || c.Env[0].ValueFrom.FieldRef.FieldPath != "metadata.uid" {
					t.Errorf("%s: collector must receive pod UID and no credentials: %+v", key, c.Env)
				}
				if len(c.VolumeMounts) != 2 {
					t.Fatalf("%s: collector must mount only its config and CA", key)
				}
				for _, mount := range c.VolumeMounts {
					if !mount.ReadOnly || (mount.Name != "collector-config" && mount.Name != "collector-ca") {
						t.Errorf("%s: unexpected collector mount: %+v", key, mount)
					}
				}
				seen := map[string]bool{}
				for _, volume := range pod.Volumes {
					switch volume.Name {
					case "collector-config":
						seen[volume.Name] = true
						if volume.ConfigMap.Name != "workouts-explorer-"+component+"-collector" {
							t.Errorf("%s: wrong collector config: %+v", key, volume)
						}
					case "collector-ca":
						seen[volume.Name] = true
						name, caKey := "workouts-explorer-database-tls", "ca.crt"
						if len(settings) == 3 {
							name, caKey = "telemetry-ca", "trust.pem"
						}
						if volume.Secret.SecretName != name || volume.Secret.DefaultMode != 0444 || len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Key != caKey || volume.Secret.Items[0].Path != "ca.crt" {
							t.Errorf("%s: collector CA projection must not include private keys: %+v", key, volume)
						}
					}
				}
				if len(seen) != 2 {
					t.Errorf("%s: collector volumes missing", key)
				}
			}
			if enabled && count != 2 {
				t.Errorf("expected exactly two collectors, got %d", count)
			}
			for _, component := range []string{"ui", "tiles"} {
				_, exists := items["ConfigMap/workouts-explorer-"+component+"-collector"]
				if exists != enabled {
					t.Errorf("%s: unexpected collector ConfigMap presence %t", component, exists)
				}
			}
		})
	}
}

func TestCollectorConfiguration(t *testing.T) {
	defaultEndpoint := "grpcs://otel-gateway-collector.observability.svc.cluster.local:4317"
	for _, endpoint := range []string{defaultEndpoint, "grpcs://gateway.example.test:4317", "grpc://gateway.example.test:4317", "https://gateway.example.test:4318", "http://gateway.example.test:4318"} {
		t.Run(endpoint, func(t *testing.T) {
			settings := []string{"otel.environment=fixture", "otel.serviceNamespace=fixture-space"}
			if endpoint != defaultEndpoint {
				settings = append(settings, "otel.endpoint="+endpoint)
			}
			items := renderCollectorChart(t, settings...)
			for _, component := range []string{"ui", "tiles"} {
				config := items["ConfigMap/workouts-explorer-"+component+"-collector"].Data["config.yaml"]
				var parsed struct {
					Receivers map[string]any
					Exporters map[string]struct {
						Endpoint string
						TLS      struct {
							Insecure bool
							CAFile   string `yaml:"ca_file"`
						}
					}
					Service struct {
						Pipelines map[string]struct {
							Receivers, Processors, Exporters []string
						}
					}
				}
				if err := yaml.Unmarshal([]byte(config), &parsed); err != nil {
					t.Fatal(err)
				}
				grpc := strings.HasPrefix(endpoint, "grpc")
				secure := strings.HasPrefix(endpoint, "grpcs:") || strings.HasPrefix(endpoint, "https:")
				exporter, wantEndpoint := "otlp_http", endpoint
				if grpc {
					exporter, wantEndpoint = "otlp_grpc", strings.TrimPrefix(strings.TrimPrefix(endpoint, "grpcs://"), "grpc://")
				}
				got := parsed.Exporters[exporter]
				if len(parsed.Exporters) != 1 || got.Endpoint != wantEndpoint || got.TLS.Insecure == secure || (got.TLS.CAFile != "") != secure {
					t.Errorf("%s: wrong exporter/TLS config: %+v", component, parsed.Exporters)
				}
				native := "nginx"
				if component == "tiles" {
					native = "prometheus/martin"
				}
				if len(parsed.Receivers) != 2 || parsed.Receivers[native] == nil || parsed.Receivers["prometheus/self"] == nil || len(parsed.Service.Pipelines) != 2 {
					t.Errorf("%s: native and self collection must be separate", component)
				}
				for _, pipeline := range []string{"native", "self"} {
					p := parsed.Service.Pipelines["metrics/"+pipeline]
					if !reflect.DeepEqual(p.Exporters, []string{exporter}) || !reflect.DeepEqual(p.Processors, []string{"memory_limiter", "resource/" + pipeline, "transform/identity", "batch"}) {
						t.Errorf("%s/%s: wrong export/identity pipeline: %+v", component, pipeline, p)
					}
				}
				for _, required := range []string{
					"value: \"workouts-" + component + "\"", "value: \"workouts-" + component + "-collector\"",
					"value: \"${env:POD_UID}\"", "value: \"fixture\"", "value: \"fixture-space\"",
					`set(datapoint.attributes["service_instance_id"], resource.attributes["service.instance.id"])`,
					`set(datapoint.attributes["deployment_environment"], resource.attributes["deployment.environment.name"])`,
					"host: 127.0.0.1", "port: 8888", "level: detailed",
				} {
					if !strings.Contains(config, required) {
						t.Errorf("%s: missing %s", component, required)
					}
				}
				if component == "ui" && !strings.Contains(config, "endpoint: http://127.0.0.1:8083/nginx_status") {
					t.Error("nginx must scrape the loopback-only status server")
				}
				if component == "tiles" {
					for _, required := range []string{"metrics_path: /_/metrics", "targets: [127.0.0.1:3000]", "source_labels: [endpoint]", "target_label: martin_route", "action: labeldrop", "regex: endpoint"} {
						if !strings.Contains(config, required) {
							t.Errorf("Martin: missing %s", required)
						}
					}
				}
				if binary := os.Getenv("OTELCOL_CONTRIB_BINARY"); binary != "" {
					path := filepath.Join(t.TempDir(), "config.yaml")
					// Validation checks config syntax without opening CA files or listeners.
					if err := os.WriteFile(path, []byte(config), 0600); err != nil {
						t.Fatal(err)
					}
					cmd := exec.Command(binary, "validate", "--config="+path)
					cmd.Env = append(os.Environ(), "POD_UID=fixture-pod-uid")
					if output, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("%s vendor config validation: %v\n%s", component, err, output)
					}
				}
			}
		})
	}
}

func TestNginxStatusIsLoopbackOnly(t *testing.T) {
	items := renderCollectorChart(t)
	chartConfig := items["ConfigMap/workouts-explorer-ui-nginx"].Data["default.conf"]
	rootConfig, err := os.ReadFile(repoPath("ui/nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	statusServer := "server {\n    listen 127.0.0.1:8083;"
	for _, config := range []string{chartConfig, string(rootConfig)} {
		parts := strings.Split(config, statusServer)
		if len(parts) != 2 || strings.Contains(parts[0], "stub_status") || !strings.Contains(parts[1], "location = /nginx_status {\n        stub_status;") || strings.Count(config, "stub_status;") != 1 {
			t.Errorf("nginx status must exist only on a separate loopback server:\n%s", config)
		}
	}
	if strings.Split(chartConfig, statusServer)[1] != strings.Split(string(rootConfig), statusServer)[1] {
		t.Error("chart and image status servers differ")
	}
	for _, template := range []string{"services.yaml", "ingress.yaml", "networkpolicy.yaml"} {
		data, err := os.ReadFile(chartPath("templates/" + template))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"8083", "8888", "13133", "nginx_status", "/_/metrics"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s must not expose collector/status ports or routes", template)
			}
		}
	}
}

func TestCollectorSchemaRejectsInvalidConfiguration(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, setting := range []string{
		"otel.endpoint=ftp://gateway.example.test:4317",
		"otel.endpoint=https://",
		"otel.endpoint=grpcs://user:password@gateway.example.test:4317",
		"otel.endpoint=grpcs://gateway.example.test:4317/path",
		"otel.endpoint=grpcs://gateway.example.test:0",
		"otel.endpoint=grpcs://gateway.example.test:65536",
		"otel.endpoint=http://gateway.example.test:0",
		"otel.endpoint=https://gateway.example.test:65536",
		"otel.collector.image=otel/opentelemetry-collector-contrib:latest",
		"otel.collector.image=otel/opentelemetry-collector-contrib:0.157.0@sha256:" + strings.Repeat("a", 64),
		"otel.trustedCASecret.key=",
		"otel.environment=",
	} {
		output, err := exec.Command("helm", "template", "invalid-test", chartDir, "--set-string", setting).CombinedOutput()
		if err == nil {
			t.Errorf("invalid configuration accepted: %s\n%s", setting, output)
		}
	}
}
