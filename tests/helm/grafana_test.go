package helm_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
)

type dashboardConfigMap struct {
	Kind     string
	Metadata struct {
		Name, Namespace     string
		Labels, Annotations map[string]string
	}
	Data map[string]string
}

type dashboardPanel struct {
	Type, Title string
	Datasource  struct{ Type, UID string }
	Targets     []struct {
		Expr       string
		Datasource struct{ Type, UID string }
	}
	Panels []dashboardPanel
}

func renderGrafanaChart(t *testing.T, namespace string, flags ...string) []dashboardConfigMap {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	args := append([]string{"template", "grafana-test", chartDir, "--namespace", namespace}, flags...)
	output, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, output)
	}
	var dashboards []dashboardConfigMap
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var item dashboardConfigMap
		if err := document.Decode(&item); err != nil {
			t.Fatal(err)
		}
		if item.Metadata.Labels["grafana_dashboard"] != "" || strings.HasSuffix(item.Metadata.Name, "-dashboard") {
			dashboards = append(dashboards, item)
		}
		if item.Kind == "Deployment" || item.Kind == "Job" {
			var pod workload
			if err := document.Decode(&pod); err != nil {
				t.Fatal(err)
			}
			for _, volume := range pod.Spec.Template.Spec.Volumes {
				if strings.HasSuffix(volume.ConfigMap.Name, "-dashboard") {
					t.Errorf("%s/%s unexpectedly mounts a dashboard ConfigMap", item.Kind, item.Metadata.Name)
				}
			}
		}
	}
	return dashboards
}

func TestGrafanaDashboardConfigMaps(t *testing.T) {
	for _, namespace := range []string{"workouts-explorer", "other-test-namespace"} {
		t.Run(namespace, func(t *testing.T) {
			items := renderGrafanaChart(t, namespace)
			if len(items) != 2 {
				t.Fatalf("expected exactly two dashboard ConfigMaps, got %d", len(items))
			}
			seen := map[string]bool{}
			for _, item := range items {
				kind := strings.TrimSuffix(strings.TrimPrefix(item.Metadata.Name, "workouts-explorer-"), "-dashboard")
				wantPanels, title := 28, "Workouts Explorer / Operational"
				if kind == "historical" {
					wantPanels, title = 20, "Workouts Explorer / Historical"
				} else if kind != "operational" {
					t.Fatalf("unexpected dashboard name %q", item.Metadata.Name)
				}
				if seen[kind] {
					t.Fatalf("duplicate dashboard %q", kind)
				}
				seen[kind] = true
				if item.Kind != "ConfigMap" || item.Metadata.Namespace != namespace {
					t.Errorf("wrong dashboard kind/namespace: %+v", item)
				}
				for key, want := range map[string]string{
					"grafana_dashboard":            "1",
					"app.kubernetes.io/name":       "workouts-explorer",
					"app.kubernetes.io/part-of":    "workouts-explorer",
					"app.kubernetes.io/instance":   "grafana-test",
					"app.kubernetes.io/managed-by": "Helm",
				} {
					if got := item.Metadata.Labels[key]; got != want {
						t.Errorf("%s label %s=%q, want %q", kind, key, got, want)
					}
				}
				if !reflect.DeepEqual(item.Metadata.Annotations, map[string]string{"vcluster.loft.sh/force-sync": "true"}) {
					t.Errorf("unexpected annotations (no hooks, keep policy or folder): %v", item.Metadata.Annotations)
				}
				content := item.Data["workouts-explorer-"+kind+".json"]
				if len(item.Data) != 1 || content == "" {
					t.Fatalf("%s must contain exactly one nonempty dashboard JSON entry", kind)
				}
				source, err := os.ReadFile(chartPath("dashboards/" + kind + ".json"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(content) != strings.TrimSpace(string(source)) {
					t.Errorf("%s rendered JSON differs from packaged source", kind)
				}
				var dashboard struct {
					UID, Title string
					Panels     []dashboardPanel
				}
				if err := json.Unmarshal([]byte(content), &dashboard); err != nil {
					t.Fatal(err)
				}
				if dashboard.UID != "workouts-explorer-"+kind || dashboard.Title != title {
					t.Errorf("%s unexpected dashboard UID/title: %s / %s", kind, dashboard.UID, dashboard.Title)
				}
				count := 0
				var visit func([]dashboardPanel)
				visit = func(panels []dashboardPanel) {
					for _, panel := range panels {
						if len(panel.Targets) > 0 {
							count++
							if panel.Datasource.Type != "prometheus" || panel.Datasource.UID != "prometheus" {
								t.Errorf("%s: incorrect panel datasource", panel.Title)
							}
							for _, target := range panel.Targets {
								if target.Expr == "" || target.Datasource.Type != "prometheus" || target.Datasource.UID != "prometheus" {
									t.Errorf("%s: empty query or incorrect target datasource", panel.Title)
								}
							}
						}
						visit(panel.Panels)
					}
				}
				visit(dashboard.Panels)
				if count != wantPanels {
					t.Errorf("%s has %d query panels, want %d (including collapsed rows)", kind, count, wantPanels)
				}
			}
		})
	}
}

func TestGrafanaDashboardsDisabled(t *testing.T) {
	for _, flags := range [][]string{
		{"--set", "grafanaDashboards.enabled=false"},
		{"--values", repoPath("deploy/acceptance/values.yaml")},
	} {
		if items := renderGrafanaChart(t, "workouts-acceptance", flags...); len(items) != 0 {
			t.Errorf("disabled dashboards still emitted: %+v", items)
		}
	}
}

func TestGrafanaDashboardSchema(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, value := range []string{"true", "false", "invalid", "1"} {
		output, err := exec.Command("helm", "template", "grafana-test", chartDir, "--set-string", "grafanaDashboards.enabled="+value).CombinedOutput()
		if err == nil || !strings.Contains(string(output), "grafanaDashboards") || !strings.Contains(string(output), "enabled") || !strings.Contains(string(output), "boolean") {
			t.Errorf("non-boolean enabled=%q did not fail schema validation: %v\n%s", value, err, output)
		}
	}
}

func TestGrafanaDashboardFilesRequired(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, kind := range []string{"operational", "historical"} {
		for _, missing := range []bool{true, false} {
			chart := filepath.Join(t.TempDir(), "chart")
			if err := os.CopyFS(chart, os.DirFS(chartDir)); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(chart, "dashboards", kind+".json")
			var err error
			if missing {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte(" \n"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("helm", "template", "grafana-test", chart).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "dashboards/"+kind+".json is required") {
				t.Errorf("missing/empty %s dashboard did not fail clearly: %v\n%s", kind, err, output)
			}
			output, err = exec.Command("helm", "template", "grafana-test", chart, "--set", "grafanaDashboards.enabled=false").CombinedOutput()
			if err != nil {
				t.Errorf("disabled dashboards must not require JSON: %v\n%s", err, output)
			}
		}
	}
}
