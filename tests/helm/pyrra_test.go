package helm_test

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
)

type sloMetric struct {
	Metric string
}

type chartSLO struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string
	Metadata   struct {
		Name, Namespace string
		Labels          map[string]string
		Annotations     map[string]string
	}
	Spec struct {
		Description, Target, Window string
		PartialResponseStrategy     string `yaml:"partial_response_strategy"`
		Alerting                    struct {
			Absent, Burnrates bool
		}
		Indicator struct {
			Ratio *struct {
				Errors, Total sloMetric
			}
			Latency *struct {
				Success, Total sloMetric
			}
		}
	}
}

func renderSLOChart(t *testing.T, flags ...string) []chartSLO {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	args := append([]string{"template", "slo-test", chartDir, "--namespace", "slo-test-namespace"}, flags...)
	output, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, output)
	}
	var slos []chartSLO
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	for {
		var item chartSLO
		if err := decoder.Decode(&item); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if item.Kind == "ServiceLevelObjective" {
			slos = append(slos, item)
		}
	}
	return slos
}

func TestPyrraSLOs(t *testing.T) {
	items := renderSLOChart(t, "--set", "pyrraSLOs.enabled=true,otel.environment=xdev,otel.serviceNamespace=custom-namespace")
	if len(items) != 4 {
		t.Fatalf("expected four SLOs, got %d", len(items))
	}
	wantTargets := map[string]string{
		"workouts-explorer-api-availability":           "99.9",
		"workouts-explorer-api-latency-500ms":          "95",
		"workouts-explorer-tile-forwarding-success":    "99.9",
		"workouts-explorer-tile-forwarding-latency-1s": "99",
	}
	for _, item := range items {
		if item.APIVersion != "pyrra.dev/v1alpha1" || item.Metadata.Namespace != "slo-test-namespace" {
			t.Errorf("incorrect resource identity: %+v", item.Metadata)
		}
		if item.Spec.Target != wantTargets[item.Metadata.Name] || item.Spec.Window != "2w" {
			t.Errorf("incorrect target/window for %s: %+v", item.Metadata.Name, item.Spec)
		}
		delete(wantTargets, item.Metadata.Name)
		for key, want := range map[string]string{
			"pyrra-rule":                 "true",
			"app.kubernetes.io/instance": "slo-test", "app.kubernetes.io/managed-by": "Helm",
		} {
			if item.Metadata.Labels[key] != want {
				t.Errorf("%s label %s: got %q, want %q", item.Metadata.Name, key, item.Metadata.Labels[key], want)
			}
		}
		for key := range item.Metadata.Labels {
			if strings.HasPrefix(key, "pyrra.dev/") {
				t.Errorf("%s must use only Pyrra's automatic namespace badge, found %s", item.Metadata.Name, key)
			}
		}
		if len(item.Metadata.Annotations) != 0 || item.Spec.PartialResponseStrategy != "abort" || !item.Spec.Alerting.Absent || !item.Spec.Alerting.Burnrates {
			t.Errorf("%s must be ordinary Helm-owned resources with abort and absent/burn alerts", item.Metadata.Name)
		}
		var numerator, total string
		if strings.Contains(item.Metadata.Name, "latency") {
			if item.Spec.Indicator.Latency == nil || item.Spec.Indicator.Ratio != nil {
				t.Fatalf("%s must use a histogram latency indicator", item.Metadata.Name)
			}
			numerator, total = item.Spec.Indicator.Latency.Success.Metric, item.Spec.Indicator.Latency.Total.Metric
		} else {
			if item.Spec.Indicator.Ratio == nil || item.Spec.Indicator.Latency != nil {
				t.Fatalf("%s must use a ratio indicator", item.Metadata.Name)
			}
			numerator, total = item.Spec.Indicator.Ratio.Errors.Metric, item.Spec.Indicator.Ratio.Total.Metric
		}
		for _, expr := range []string{numerator, total} {
			for _, scope := range []string{`service_name="workouts-api"`, `service_namespace="custom-namespace"`, `deployment_environment="xdev"`} {
				if !strings.Contains(expr, scope) {
					t.Errorf("unscoped metric: %s", expr)
				}
			}
			if strings.Contains(expr, "exported_job") || strings.Contains(expr, "increase(") || strings.Contains(expr, "rate(") || strings.Contains(expr, "vector(0)") {
				t.Errorf("input must be a producer-scoped raw selector: %s", expr)
			}
		}
		switch item.Metadata.Name {
		case "workouts-explorer-api-availability":
			if !strings.Contains(numerator, `http_response_status_code=~"5.."`) || !strings.Contains(total, `http_response_status_code=~"2..|3..|5.."`) {
				t.Error("API availability population mismatch")
			}
		case "workouts-explorer-api-latency-500ms":
			if !strings.Contains(numerator, `le="0.5"`) || !strings.HasPrefix(numerator, "http_server_request_duration_seconds_bucket{") {
				t.Error("API latency must use the exact 500ms bucket")
			}
		case "workouts-explorer-tile-forwarding-success":
			if !strings.Contains(numerator, `outcome!="success"`) || strings.Contains(total, "outcome=") || strings.Contains(numerator, "http_response_status_code") {
				t.Error("forwarding success must use all outcomes, not HTTP status")
			}
		case "workouts-explorer-tile-forwarding-latency-1s":
			if !strings.Contains(numerator, `le="1.0"`) || !strings.Contains(numerator, `outcome="success"`) || !strings.Contains(total, `outcome="success"`) {
				t.Error("forwarding latency populations must contain only successful forwards")
			}
		}
		if strings.Contains(item.Metadata.Name, "api-") {
			for _, expr := range []string{numerator, total} {
				if !strings.Contains(expr, `http_route=~"/api/.*"`) || !strings.Contains(expr, `http_request_method="GET"`) {
					t.Errorf("API selector must exclude probes and non-GET operations: %s", expr)
				}
			}
		}
	}
	if len(wantTargets) != 0 {
		t.Errorf("missing SLOs: %v", wantTargets)
	}
}

func TestPyrraSLOsOptIn(t *testing.T) {
	for _, flags := range [][]string{nil, {"--values", repoPath("deploy/acceptance/values.yaml")}} {
		if items := renderSLOChart(t, flags...); len(items) != 0 {
			t.Errorf("default/acceptance chart created %d SLOs", len(items))
		}
	}
	if items := renderSLOChart(t, "--set", "pyrraSLOs.enabled=true,otel.environment=xdev"); len(items) != 4 {
		t.Errorf("xdev must enable the four SLOs, got %d", len(items))
	}
}

func TestPyrraSLOBacksyncGuard(t *testing.T) {
	items := renderSLOChart(t, "--set", "pyrraSLOs.enabled=true,pyrraSLOs.skipBacksync=true")
	if len(items) != 4 {
		t.Fatalf("expected four guarded SLOs, got %d", len(items))
	}
	for _, item := range items {
		if len(item.Metadata.Annotations) != 1 || item.Metadata.Annotations["vcluster.loft.sh/skip-backsync"] != "true" {
			t.Errorf("%s missing generated-ConfigMap backsync guard", item.Metadata.Name)
		}
		if item.Metadata.Labels["pyrra-rule"] != "true" || item.Spec.PartialResponseStrategy != "abort" {
			t.Errorf("%s guard must preserve discovery and evaluation policy", item.Metadata.Name)
		}
		for key := range item.Metadata.Labels {
			if strings.HasPrefix(key, "pyrra.dev/") {
				t.Errorf("%s backsync guard must preserve namespace-only badges, found %s", item.Metadata.Name, key)
			}
		}
	}
}

func TestPyrraSLOValidation(t *testing.T) {
	for _, setting := range []string{
		"pyrraSLOs.enabled=invalid", "pyrraSLOs.skipBacksync=invalid", "pyrraSLOs.window=6h",
		"pyrraSLOs.targets.apiAvailability=100", "pyrraSLOs.targets.tileSuccess=0",
		"pyrraSLOs.targets.apiLatency=-1", "pyrraSLOs.targets.tileLatency=101",
	} {
		output, err := exec.Command("helm", "template", "slo-test", chartDir, "--set-string", setting).CombinedOutput()
		if err == nil || !strings.Contains(string(output), "pyrraSLOs") {
			t.Errorf("invalid setting %s accepted: %v\n%s", setting, err, output)
		}
	}
	output, err := exec.Command("helm", "template", "slo-test", chartDir, "--set", "pyrraSLOs.enabled=true", "--set-string", "otel.endpoint=").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "pyrraSLOs.enabled requires otel.endpoint") {
		t.Errorf("SLOs with disabled telemetry accepted: %v\n%s", err, output)
	}
	items := renderSLOChart(t, "--set", "pyrraSLOs.enabled=true", "--set-string", "pyrraSLOs.window=4w,pyrraSLOs.targets.apiAvailability=99.95")
	if len(items) != 4 || items[0].Spec.Target != "99.95" || items[0].Spec.Window != "4w" {
		t.Errorf("configured target/window not retained: %+v", items)
	}
}

func TestGoMetricOriginEnvironment(t *testing.T) {
	items := renderCollectorChart(t, "otel.environment=xdev", "otel.serviceNamespace=custom-namespace")
	for _, component := range []string{"api", "worker", "coverage-worker"} {
		item := items["Deployment/workouts-explorer-"+component]
		uid, identity := -1, -1
		for i, env := range item.Spec.Template.Spec.Containers[0].Env {
			switch env.Name {
			case "POD_UID":
				uid = i
				if env.ValueFrom.FieldRef.FieldPath != "metadata.uid" {
					t.Errorf("%s must use the originating pod UID", component)
				}
			case "OTEL_RESOURCE_ATTRIBUTES":
				identity = i
				if env.Value != "service.namespace=custom-namespace,service.instance.id=$(POD_UID),deployment.environment.name=xdev" {
					t.Errorf("%s incorrect metric identity environment: %s", component, env.Value)
				}
			}
		}
		if uid < 0 || identity <= uid {
			t.Errorf("%s must define POD_UID before expanding it in OTEL_RESOURCE_ATTRIBUTES", component)
		}
	}
}
