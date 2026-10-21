package helm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestHelmAPIHookDatabaseSecrets(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, enabled := range []bool{false, true} {
		for _, custom := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled=%t/custom=%t", enabled, custom), func(t *testing.T) {
				args := []string{"template", "hook-config-test", chartDir}
				if enabled {
					args = append(args, "--set", "api.roleProvisioning.enabled=true,api.bootstrap.enabled=true")
				}
				refs := map[string]struct{ Name, Key string }{
					"api":               {"workouts-explorer-database", "apiDatabaseUrl"},
					"migration":         {"workouts-explorer-database", "migrationDatabaseUrl"},
					"role-provisioning": {"workouts-explorer-database", "migrationDatabaseUrl"},
					"bootstrap-admin":   {"workouts-explorer-database", "migrationDatabaseUrl"},
				}
				adminSecret := "workouts-explorer-bootstrap-admin"
				if custom {
					for component, path := range map[string]string{
						"api": "api.databaseSecret", "migration": "api.migration.databaseSecret",
						"role-provisioning": "api.roleProvisioning.databaseSecret", "bootstrap-admin": "api.bootstrap.databaseSecret",
					} {
						ref := struct{ Name, Key string }{"fixture-" + component + "-db", "fixture-" + component + "-url"}
						refs[component] = ref
						args = append(args, "--set-string", path+".name="+ref.Name, "--set-string", path+".key="+ref.Key)
					}
					adminSecret = "fixture-admin-credentials"
					args = append(args, "--set-string", "api.bootstrap.secretName="+adminSecret)
				}
				output, err := exec.Command("helm", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("render: %v\n%s", err, output)
				}
				if bytes.Contains(output, []byte("workouts-explorer-role-provisioning")) {
					t.Error("chart references a phantom role-provisioning secret")
				}
				decoder := yaml.NewDecoder(bytes.NewReader(output))
				seen := map[string]bool{}
				for {
					var item workload
					if err := decoder.Decode(&item); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					if item.Kind == "Secret" {
						t.Error("chart must not create credentials")
					}
					component := item.Spec.Template.Metadata.Labels["app.kubernetes.io/component"]
					ref, ok := refs[component]
					if !ok {
						continue
					}
					seen[component] = true
					variable := map[string]string{"api": "API_DATABASE_URL", "migration": "MIGRATION_DATABASE_URL", "role-provisioning": "ROLE_PROVISIONING_DATABASE_URL", "bootstrap-admin": "BOOTSTRAP_DATABASE_URL"}[component]
					found := 0
					adminFields := map[string]string{"BOOTSTRAP_USERNAME": "username", "BOOTSTRAP_EMAIL": "email"}
					for _, c := range item.Spec.Template.Spec.Containers {
						for _, env := range c.Env {
							if env.Name == variable {
								found++
								if env.Value != "" || env.ValueFrom.SecretKeyRef != ref {
									t.Errorf("%s: database selector=%v, want %v", component, env.ValueFrom.SecretKeyRef, ref)
								}
							}
							if component == "api" && (env.Name == "MIGRATION_DATABASE_URL" || env.Name == "ROLE_PROVISIONING_DATABASE_URL" || env.Name == "BOOTSTRAP_DATABASE_URL") {
								t.Error("runtime API must not receive privileged hook credentials")
							}
							if component == "bootstrap-admin" {
								if key, ok := adminFields[env.Name]; ok {
									if env.ValueFrom.SecretKeyRef != (struct{ Name, Key string }{adminSecret, key}) {
										t.Errorf("%s: admin selector changed", env.Name)
									}
									delete(adminFields, env.Name)
								}
							}
						}
						if component == "api" && custom {
							for _, other := range []string{"migration", "role-provisioning", "bootstrap-admin"} {
								for _, env := range c.Env {
									if env.ValueFrom.SecretKeyRef.Name == refs[other].Name || env.ValueFrom.SecretKeyRef.Name == adminSecret {
										t.Errorf("API references %s hook credentials", other)
									}
								}
							}
						}
					}
					if found != 1 {
						t.Errorf("%s: expected exactly one %s, got %d", component, variable, found)
					}
					if component == "bootstrap-admin" {
						if len(adminFields) != 0 {
							t.Error("missing admin username/email selectors")
						}
						passwordSeen := false
						for _, volume := range item.Spec.Template.Spec.Volumes {
							if volume.Name == "source" {
								passwordSeen = true
								if volume.Secret.SecretName != adminSecret || len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Key != "password" || volume.Secret.Items[0].Path != "password" {
									t.Error("admin password selector changed")
								}
							}
						}
						if !passwordSeen {
							t.Error("missing admin password secret volume")
						}
					}
				}
				if !seen["api"] || !seen["migration"] || seen["role-provisioning"] != enabled || seen["bootstrap-admin"] != enabled {
					t.Errorf("unexpected hook presence: %v", seen)
				}
				if !enabled && bytes.Contains(output, []byte(adminSecret)) {
					t.Error("disabled bootstrap must not reference admin credentials")
				}
			})
		}
	}
}

func TestHelmCoverageWorkerJobConcurrency(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, raw := range []string{"", "1", "8"} {
		for _, readerOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("concurrency=%s/policy=%t", raw, readerOnly), func(t *testing.T) {
				args := []string{"template", "concurrency-test", chartDir, "--set", "osm.useReadReplicasOnly=" + strconv.FormatBool(readerOnly)}
				want := "2"
				if raw != "" {
					want = raw
					args = append(args, "--set", "coverageWorker.jobConcurrency="+raw)
				}
				output, err := exec.Command("helm", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("render: %v\n%s", err, output)
				}
				decoder := yaml.NewDecoder(bytes.NewReader(output))
				found := 0
				for {
					var item workload
					if err := decoder.Decode(&item); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					if item.Kind == "Deployment" && item.Spec.Replicas != 1 {
						t.Error("job concurrency must not change replica counts")
					}
					for _, c := range item.Spec.Template.Spec.Containers {
						for _, env := range c.Env {
							if env.Name == "COVERAGE_WORKER_JOB_CONCURRENCY" {
								found++
								if c.Name != "coverage-worker" || env.Value != want || env.ValueFrom.SecretKeyRef.Name != "" {
									t.Errorf("unexpected concurrency env: %+v", env)
								}
							}
						}
					}
					if item.Kind == "ConfigMap" && item.Data["COVERAGE_DIAGNOSTICS_CONCURRENCY"] != "" && item.Data["COVERAGE_DIAGNOSTICS_CONCURRENCY"] != "1" {
						t.Error("worker concurrency changed API diagnostic concurrency")
					}
				}
				if found != 1 {
					t.Errorf("expected exactly one worker concurrency env, got %d", found)
				}
			})
		}
	}
}

func TestHelmConfigurationSchema(t *testing.T) {
	rawSchema, err := os.ReadFile(chartPath("values.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(rawSchema, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("values.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	rawValues, err := os.ReadFile(chartPath("values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var defaults map[string]any
	if err := yaml.Unmarshal(rawValues, &defaults); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(defaults); err != nil {
		t.Fatal(err)
	}
	worker := defaults["coverageWorker"].(map[string]any)
	if worker["jobConcurrency"] != 2 || worker["replicas"] != 1 {
		t.Error("expected default job concurrency 2 and one replica")
	}
	workerProperties := document["properties"].(map[string]any)["coverageWorker"].(map[string]any)["properties"].(map[string]any)
	if workerProperties["jobConcurrency"].(map[string]any)["default"] != float64(2) {
		t.Error("schema must declare default job concurrency 2")
	}
	api := defaults["api"].(map[string]any)
	apiProperties := document["properties"].(map[string]any)["api"].(map[string]any)["properties"].(map[string]any)
	for _, hook := range []string{"migration", "roleProvisioning", "bootstrap"} {
		parent := api[hook].(map[string]any)
		if !reflect.DeepEqual(parent["databaseSecret"], map[string]any{"name": "workouts-explorer-database", "key": "migrationDatabaseUrl"}) {
			t.Errorf("%s: unexpected default database ref", hook)
		}
		properties := apiProperties[hook].(map[string]any)["properties"].(map[string]any)
		if properties["databaseSecret"].(map[string]any)["$ref"] != "#/$defs/secretRef" {
			t.Errorf("%s must use the shared strict secretRef definition", hook)
		}
		if hook != "migration" && parent["enabled"] != false {
			t.Errorf("%s must default to disabled", hook)
		}
	}
	type schemaCase struct {
		path   string
		value  any
		remove bool
		valid  bool
	}
	cases := []schemaCase{{path: "coverageWorker.jobConcurrency", remove: true}}
	for _, value := range []any{1, 2, 8} {
		cases = append(cases, schemaCase{path: "coverageWorker.jobConcurrency", value: value, valid: true})
	}
	for _, value := range []any{0, -1, 9, 1.5, "2", "", true, nil} {
		cases = append(cases, schemaCase{path: "coverageWorker.jobConcurrency", value: value})
	}
	for _, path := range []string{"api.migrationDatabaseKey", "api.roleProvisioning.secretName", "api.roleProvisioning.databaseKey", "api.bootstrap.databaseKey", "api.migration.unknown"} {
		cases = append(cases, schemaCase{path: path, value: "fixture-removed"})
	}
	for _, hook := range []string{"migration", "roleProvisioning", "bootstrap"} {
		path := "api." + hook + ".databaseSecret"
		cases = append(cases, schemaCase{path: "api." + hook, remove: true}, schemaCase{path: path, remove: true})
		cases = append(cases, schemaCase{path: path, value: map[string]any{"name": "fixture-" + hook, "key": "fixture-url"}, valid: true})
		for _, value := range []any{nil, "fixture-secret", []any{}, map[string]any{}, map[string]any{"name": "fixture-secret", "key": "fixture-url", "unknown": true}} {
			cases = append(cases, schemaCase{path: path, value: value})
		}
		for _, leaf := range []string{"name", "key"} {
			cases = append(cases, schemaCase{path: path + "." + leaf, remove: true})
			for _, value := range []any{"", nil, 1, true, []any{}, map[string]any{}} {
				cases = append(cases, schemaCase{path: path + "." + leaf, value: value})
			}
		}
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/remove=%t/value=%T:%v", tc.path, tc.remove, tc.value, tc.value), func(t *testing.T) {
			var values map[string]any
			if err := yaml.Unmarshal(rawValues, &values); err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(tc.path, ".")
			parent := values
			for _, part := range parts[:len(parts)-1] {
				parent = parent[part].(map[string]any)
			}
			if tc.remove {
				delete(parent, parts[len(parts)-1])
			} else {
				parent[parts[len(parts)-1]] = tc.value
			}
			if err := schema.Validate(values); (err == nil) != tc.valid {
				t.Fatalf("schema valid=%t want %t: %v", err == nil, tc.valid, err)
			}
		})
	}
}
