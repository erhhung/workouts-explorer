package helm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestHelmOSMReadReplicaRouting(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, tc := range []struct {
		name          string
		diagnostics   bool
		customReaders bool
		customWriter  bool
	}{
		{"defaults", false, false, false},
		{"default diagnostics", true, false, false},
		{"custom readers", true, true, false},
		{"custom writer", true, false, true},
		{"mixed selectors and TLS", true, true, true},
		{"mixed without diagnostics", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var defaultPolicyManifests []map[string]any
			for _, readerOnly := range []bool{false, true} {
				t.Run("policy="+strconv.FormatBool(readerOnly), func(t *testing.T) {
					args := []string{"template", "osm-replica-test", chartDir}
					if tc.name != "defaults" || readerOnly {
						args = append(args, "--set", "osm.useReadReplicasOnly="+strconv.FormatBool(readerOnly), "--set", "api.coverageDiagnostics.enabled="+strconv.FormatBool(tc.diagnostics))
					}
					apiOSMKey, coverageOSMKey, workerOSMKey := "osmReaderDatabaseUrl", "osmReaderDatabaseUrl", "osmReaderDatabaseUrl"
					apiOSMSecret, coverageOSMSecret, workerOSMSecret := "workouts-explorer-database", "workouts-explorer-database", "workouts-explorer-database"
					apiSecret, coverageSecret, workerSecret := "workouts-explorer-database", "workouts-explorer-database", "workouts-explorer-database"
					apiKey, coverageKey, workerKey := "apiDatabaseUrl", "coverageWorkerDatabaseUrl", "workerDatabaseUrl"
					apiTLS, coverageTLS, workerTLS := "workouts-explorer-database-tls", "workouts-explorer-database-tls", "workouts-explorer-database-tls"
					writerSecret, writerKey, writerTLS := "workouts-explorer-database", "osmDatabaseUrl", "workouts-explorer-database-tls"
					if tc.customReaders {
						apiOSMKey, coverageOSMKey, workerOSMKey = "fixtureApiOSM", "fixtureCovOSM", "fixtureWorkerOSM"
						apiOSMSecret, coverageOSMSecret, workerOSMSecret = "fixture-api-osm-reader", "fixture-coverage-osm-reader", "fixture-worker-osm-reader"
						apiSecret, coverageSecret, workerSecret = "fixture-api-db", "fixture-coverage-db", "fixture-worker-db"
						apiKey, coverageKey, workerKey = "fixtureApiDB", "fixtureCovDB", "fixtureWorkerDB"
						apiTLS, coverageTLS, workerTLS = "fixtureApiTLS", "fixtureCovTLS", "fixtureWorkerTLS"
						for _, setting := range []string{
							"api.coverageDiagnostics.databaseSecret.name=" + apiOSMSecret,
							"api.coverageDiagnostics.databaseSecret.key=" + apiOSMKey,
							"coverageWorker.osmDatabaseSecret.name=" + coverageOSMSecret,
							"coverageWorker.osmDatabaseSecret.key=" + coverageOSMKey,
							"worker.osmDatabaseSecret.name=" + workerOSMSecret,
							"worker.osmDatabaseSecret.key=" + workerOSMKey,
							"api.databaseSecret.name=" + apiSecret,
							"coverageWorker.databaseSecret.name=" + coverageSecret,
							"worker.databaseSecret.name=" + workerSecret,
							"api.databaseSecret.key=" + apiKey,
							"coverageWorker.databaseSecret.key=" + coverageKey,
							"worker.databaseSecret.key=" + workerKey,
							"api.coverageDiagnostics.databaseTLSSecret=" + apiTLS,
							"coverageWorker.databaseTLSSecret=" + coverageTLS,
							"worker.databaseTLSSecret=" + workerTLS,
						} {
							args = append(args, "--set-string", setting)
						}
					}
					if tc.customWriter {
						writerSecret, writerKey, writerTLS = "fixtureOsmWriter", "fixtureWriterOSM", "fixtureWriterTLS"
						for _, setting := range []string{
							"osm.writerDatabaseSecret.name=" + writerSecret,
							"osm.writerDatabaseSecret.key=" + writerKey,
							"osm.writerDatabaseTLSSecret=" + writerTLS,
						} {
							args = append(args, "--set-string", setting)
						}
					}
					output, err := exec.Command("helm", args...).CombinedOutput()
					if err != nil {
						t.Fatalf("render: %v\n%s", err, output)
					}
					decoder := yaml.NewDecoder(bytes.NewReader(output))
					seen := map[string]bool{}
					flags := 0
					for {
						var item struct {
							Kind string
							Spec struct {
								Replicas int
								Template struct {
									Metadata struct{ Labels map[string]string }
									Spec     struct {
										Containers []struct {
											Env []struct {
												Name      string
												Value     string
												ValueFrom struct {
													SecretKeyRef struct{ Name, Key string } `yaml:"secretKeyRef"`
												} `yaml:"valueFrom"`
											}
											VolumeMounts []struct {
												Name      string
												MountPath string `yaml:"mountPath"`
												ReadOnly  bool   `yaml:"readOnly"`
											} `yaml:"volumeMounts"`
										}
										Volumes []struct {
											Name   string
											Secret struct {
												SecretName  string `yaml:"secretName"`
												DefaultMode int    `yaml:"defaultMode"`
											}
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
						if item.Kind != "Deployment" && item.Kind != "Job" {
							continue
						}
						component := item.Spec.Template.Metadata.Labels["app.kubernetes.io/component"]
						seen[component] = true
						if item.Kind == "Deployment" && item.Spec.Replicas != 1 {
							t.Errorf("%s: replica count changed", component)
						}
						want := map[string]struct{ Name, Key string }{}
						osmEnv, osmKey, secret := "", "", ""
						tlsVolume, tlsSecret := "", ""
						switch component {
						case "api":
							want["API_DATABASE_URL"] = struct{ Name, Key string }{apiSecret, apiKey}
							if tc.diagnostics {
								osmEnv, osmKey, secret = "API_OSM_DATABASE_URL", apiOSMKey, apiOSMSecret
								tlsVolume, tlsSecret = "diagnostics-database-tls", apiTLS
							}
						case "coverage-worker":
							want["COVERAGE_WORKER_DATABASE_URL"] = struct{ Name, Key string }{coverageSecret, coverageKey}
							osmEnv, osmKey, secret = "COVERAGE_WORKER_OSM_DATABASE_URL", coverageOSMKey, coverageOSMSecret
							tlsVolume, tlsSecret = "database-tls", coverageTLS
						case "worker":
							want["WORKER_DATABASE_URL"] = struct{ Name, Key string }{workerSecret, workerKey}
							osmEnv, osmKey, secret = "OSM_DATABASE_URL", workerOSMKey, workerOSMSecret
							tlsVolume, tlsSecret = "database-tls", workerTLS
						case "tiles":
							want["MARTIN_DATABASE_URL"] = struct{ Name, Key string }{"workouts-explorer-database", "tilesDatabaseUrl"}
						case "migration":
							want["MIGRATION_DATABASE_URL"] = struct{ Name, Key string }{"workouts-explorer-database", "migrationDatabaseUrl"}
						case "osm-migration", "timezone-import":
							want["OSM_MIGRATION_DATABASE_URL"] = struct{ Name, Key string }{writerSecret, writerKey}
							tlsVolume, tlsSecret = "database-tls", writerTLS
						}
						if osmEnv != "" {
							want[osmEnv] = struct{ Name, Key string }{secret, osmKey}
						}
						flagSeen, autoAddSeen, tlsMountSeen, tlsVolumeSeen := false, false, false, false
						concurrencySeen := false
						mounts := map[string]bool{}
						for _, c := range item.Spec.Template.Spec.Containers {
							for _, env := range c.Env {
								if env.Name == "COVERAGE_WORKER_JOB_CONCURRENCY" {
									if concurrencySeen || component != "coverage-worker" || env.Value != "2" {
										t.Errorf("%s: unexpected job concurrency %q", component, env.Value)
									}
									concurrencySeen = true
								}
								if ref, ok := want[env.Name]; ok {
									if env.ValueFrom.SecretKeyRef != ref || env.Value != "" {
										t.Errorf("%s/%s: selector=%v, want %v", component, env.Name, env.ValueFrom.SecretKeyRef, ref)
									}
									delete(want, env.Name)
								} else if component == "api" && env.Name == "API_OSM_DATABASE_URL" && !tc.diagnostics {
									t.Error("disabled diagnostics must not select an OSM credential")
								}
								if env.Name == "OSM_USE_READ_REPLICAS_ONLY" {
									flags++
									if flagSeen || (component != "api" && component != "coverage-worker" && component != "worker") || env.Value != strconv.FormatBool(readerOnly) {
										t.Errorf("%s: unexpected reader flag %q", component, env.Value)
									}
									flagSeen = true
								}
								if env.Name == "OSM_AUTO_ADD_REGIONS" && (component == "coverage-worker" || component == "worker") {
									autoAddSeen = true
									if env.Value != "false" {
										t.Errorf("%s: auto-add default changed", component)
									}
								}
							}
							for _, mount := range c.VolumeMounts {
								mounts[mount.Name] = true
								if tlsVolume != "" && mount.Name == tlsVolume {
									if tlsMountSeen || mount.MountPath != "/tls/prod-db" || !mount.ReadOnly {
										t.Errorf("%s: unexpected database TLS mount: %+v", component, mount)
									}
									tlsMountSeen = true
								}
							}
						}
						if len(want) != 0 {
							t.Errorf("%s: missing database selectors: %v", component, want)
						}
						if (component == "api" || component == "coverage-worker" || component == "worker") && !flagSeen {
							t.Errorf("%s: missing reader flag", component)
						}
						if (component == "coverage-worker" || component == "worker") && !autoAddSeen {
							t.Errorf("%s: missing auto-add policy", component)
						}
						if component == "api" && mounts["diagnostics-database-tls"] != tc.diagnostics {
							t.Error("API diagnostics TLS mount must depend only on diagnostics.enabled")
						}
						for _, volume := range item.Spec.Template.Spec.Volumes {
							if component == "api" && volume.Name == "diagnostics-database-tls" && !tc.diagnostics {
								t.Error("disabled diagnostics must not select an OSM TLS secret")
							}
							if tlsVolume != "" && volume.Name == tlsVolume {
								if tlsVolumeSeen || volume.Secret.SecretName != tlsSecret || volume.Secret.DefaultMode != 0440 {
									t.Errorf("%s: TLS secret=%+v, want %s with mode 0440", component, volume.Secret, tlsSecret)
								}
								tlsVolumeSeen = true
							}
							if !mounts[volume.Name] {
								t.Errorf("%s: unmounted volume %s", component, volume.Name)
							}
						}
						if tlsVolume != "" && (!tlsMountSeen || !tlsVolumeSeen) {
							t.Errorf("%s: missing database TLS mount or volume", component)
						}
						if component == "coverage-worker" && mounts["source-keyring"] {
							t.Error("coverage worker must not mount a source keyring")
						}
						if component == "coverage-worker" && !concurrencySeen {
							t.Error("coverage worker must receive default job concurrency")
						}
					}
					if flags != 3 {
						t.Errorf("expected exactly three reader flags and no writer flags, got %d", flags)
					}
					for _, component := range []string{"api", "coverage-worker", "worker", "tiles", "osm-migration", "timezone-import"} {
						if !seen[component] {
							t.Errorf("missing workload %s", component)
						}
					}
					// Compare complete manifests after normalizing only the native reader policy flag.
					decoder = yaml.NewDecoder(bytes.NewReader(output))
					var manifests []map[string]any
					for {
						var item map[string]any
						if err := decoder.Decode(&item); err == io.EOF {
							break
						} else if err != nil {
							t.Fatal(err)
						}
						if item["kind"] == "Deployment" || item["kind"] == "Job" {
							pod := item["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
							for _, c := range pod["containers"].([]any) {
								env, _ := c.(map[string]any)["env"].([]any)
								for _, entry := range env {
									e := entry.(map[string]any)
									if e["name"] == "OSM_USE_READ_REPLICAS_ONLY" {
										e["value"] = "false"
									}
								}
							}
						}
						manifests = append(manifests, item)
					}
					if !readerOnly {
						defaultPolicyManifests = manifests
					} else if !reflect.DeepEqual(manifests, defaultPolicyManifests) {
						t.Error("changing osm.useReadReplicasOnly must change only the native reader policy env flags")
					}
				})
			}
		})
	}
}

func TestHelmOSMReadReplicaSchema(t *testing.T) {
	rawSchema, err := os.ReadFile(chartPath("values.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document any
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
	readerPaths := []string{"api.coverageDiagnostics.databaseSecret", "worker.osmDatabaseSecret", "coverageWorker.osmDatabaseSecret"}
	t.Run("defaults", func(t *testing.T) {
		var values map[string]any
		if err := yaml.Unmarshal(rawValues, &values); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(values); err != nil {
			t.Fatal(err)
		}
		osm := values["osm"].(map[string]any)
		if osm["useReadReplicasOnly"] != false {
			t.Error("reader-only policy must default to false")
		}
		if _, ok := osm["readerDatabaseKey"]; ok {
			t.Error("removed global reader key must not appear in default values")
		}
		if want := map[string]any{"name": "workouts-explorer-database", "key": "osmDatabaseUrl"}; !reflect.DeepEqual(osm["writerDatabaseSecret"], want) {
			t.Errorf("writer secret=%v, want %v", osm["writerDatabaseSecret"], want)
		}
		if osm["writerDatabaseTLSSecret"] != "workouts-explorer-database-tls" {
			t.Errorf("unexpected writer TLS default: %v", osm["writerDatabaseTLSSecret"])
		}
		for _, path := range readerPaths {
			var value any = values
			properties := document.(map[string]any)["properties"].(map[string]any)
			parts := strings.Split(path, ".")
			for _, part := range parts[:len(parts)-1] {
				value = value.(map[string]any)[part]
				properties = properties[part].(map[string]any)["properties"].(map[string]any)
			}
			parent := value.(map[string]any)
			value = parent[parts[len(parts)-1]]
			if want := map[string]any{"name": "workouts-explorer-database", "key": "osmReaderDatabaseUrl"}; !reflect.DeepEqual(value, want) {
				t.Errorf("%s=%v, want %v", path, value, want)
			}
			if _, ok := parent["osmDatabaseKey"]; ok {
				t.Errorf("%s: removed osmDatabaseKey must not appear in default values", path)
			}
			if _, ok := properties["osmDatabaseKey"]; ok {
				t.Errorf("%s: removed osmDatabaseKey must not be declared in the schema", path)
			}
		}
		osmProperties := document.(map[string]any)["properties"].(map[string]any)["osm"].(map[string]any)["properties"].(map[string]any)
		if osmProperties["useReadReplicasOnly"].(map[string]any)["default"] != false {
			t.Error("schema reader-only policy must default to false")
		}
		if _, ok := osmProperties["readerDatabaseKey"]; ok {
			t.Error("removed global reader key must not be declared in the schema")
		}
	})
	t.Run("removed selector helper", func(t *testing.T) {
		for _, path := range []string{"templates/_helpers.tpl", "templates/deployments.yaml"} {
			raw, err := os.ReadFile(chartPath(path))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("workouts.osmReaderDatabaseKey")) {
				t.Errorf("%s must not define or use the removed policy-dependent selector helper", path)
			}
		}
	})
	type schemaCase struct {
		name    string
		changes map[string]any
		remove  []string
		valid   bool
	}
	tests := []schemaCase{
		{name: "explicit false", changes: map[string]any{"osm.useReadReplicasOnly": false}, valid: true},
		{name: "reader only", changes: map[string]any{"osm.useReadReplicasOnly": true}, valid: true},
		{name: "custom writer", changes: map[string]any{"osm.writerDatabaseSecret": map[string]any{"name": "fixtureOsmWriter", "key": "fixtureWriterOSM"}, "osm.writerDatabaseTLSSecret": "fixtureWriterTLS"}, valid: true},
		{name: "auto-add without reader policy", changes: map[string]any{"osm.autoAddRegions": true}, valid: true},
		{name: "auto-add conflict", changes: map[string]any{"osm.useReadReplicasOnly": true, "osm.autoAddRegions": true}},
		{name: "string boolean", changes: map[string]any{"osm.useReadReplicasOnly": "true"}},
		{name: "numeric boolean", changes: map[string]any{"osm.useReadReplicasOnly": 1}},
		{name: "null boolean", changes: map[string]any{"osm.useReadReplicasOnly": nil}},
		{name: "removed global reader string", changes: map[string]any{"osm.readerDatabaseKey": "customReader"}},
		{name: "removed global reader non-string", changes: map[string]any{"osm.useReadReplicasOnly": true, "osm.readerDatabaseKey": true}},
		{name: "removed global reader empty", changes: map[string]any{"osm.readerDatabaseKey": ""}},
		{name: "removed global reader null", changes: map[string]any{"osm.readerDatabaseKey": nil}},
		{name: "misspelled flag", changes: map[string]any{"osm.UseReadReplicasOnly": true}},
		{name: "misspelled key", changes: map[string]any{"osm.readerDatabasekey": "customReader"}},
		{name: "missing writer secret", remove: []string{"osm.writerDatabaseSecret"}},
		{name: "missing writer TLS", remove: []string{"osm.writerDatabaseTLSSecret"}},
		{name: "empty writer secret", changes: map[string]any{"osm.writerDatabaseSecret": map[string]any{}}},
		{name: "string writer secret", changes: map[string]any{"osm.writerDatabaseSecret": "fixtureOsmWriter"}},
		{name: "null writer secret", changes: map[string]any{"osm.writerDatabaseSecret": nil}},
		{name: "array writer secret", changes: map[string]any{"osm.writerDatabaseSecret": []any{"fixtureOsmWriter", "fixtureWriterOSM"}}},
		{name: "missing writer name", remove: []string{"osm.writerDatabaseSecret.name"}},
		{name: "missing writer key", remove: []string{"osm.writerDatabaseSecret.key"}},
		{name: "empty writer name", changes: map[string]any{"osm.writerDatabaseSecret.name": ""}},
		{name: "empty writer key", changes: map[string]any{"osm.writerDatabaseSecret.key": ""}},
		{name: "non-string writer name", changes: map[string]any{"osm.writerDatabaseSecret.name": true}},
		{name: "non-string writer key", changes: map[string]any{"osm.writerDatabaseSecret.key": 1}},
		{name: "unknown writer property", changes: map[string]any{"osm.writerDatabaseSecret.readerKey": "customReader"}},
		{name: "empty writer TLS", changes: map[string]any{"osm.writerDatabaseTLSSecret": ""}},
		{name: "non-string writer TLS", changes: map[string]any{"osm.writerDatabaseTLSSecret": true}},
		{name: "null writer TLS", changes: map[string]any{"osm.writerDatabaseTLSSecret": nil}},
	}
	for _, path := range readerPaths {
		for _, readerOnly := range []bool{false, true} {
			diagnosticsStates := []bool{false}
			if strings.HasPrefix(path, "api.") {
				diagnosticsStates = append(diagnosticsStates, true)
			}
			for _, diagnostics := range diagnosticsStates {
				prefix := path + "/policy=" + strconv.FormatBool(readerOnly) + "/diagnostics=" + strconv.FormatBool(diagnostics)
				cases := []schemaCase{
					{name: "custom reader", changes: map[string]any{path: map[string]any{"name": "fixture-reader-secret", "key": "fixtureReaderKey"}}, valid: true},
					{name: "missing reader secret", remove: []string{path}},
					{name: "empty reader secret", changes: map[string]any{path: map[string]any{}}},
					{name: "string reader secret", changes: map[string]any{path: "fixture-reader-secret"}},
					{name: "null reader secret", changes: map[string]any{path: nil}},
					{name: "array reader secret", changes: map[string]any{path: []any{"fixture-reader-secret", "fixtureReaderKey"}}},
					{name: "unknown reader property", changes: map[string]any{path + ".readerKey": "fixtureReaderKey"}},
				}
				for _, leaf := range []string{"name", "key"} {
					cases = append(cases, schemaCase{name: "missing reader " + leaf, remove: []string{path + "." + leaf}})
					for _, value := range []any{"", true, 1, nil, []any{}, map[string]any{}} {
						cases = append(cases, schemaCase{
							name:    "invalid reader " + leaf + "/" + fmt.Sprintf("%T=%v", value, value),
							changes: map[string]any{path + "." + leaf: value},
						})
					}
				}
				parent := path[:strings.LastIndex(path, ".")]
				for _, value := range []any{"customReader", "", true, 1, nil} {
					cases = append(cases, schemaCase{
						name:    "removed key/" + fmt.Sprintf("%T=%v", value, value),
						changes: map[string]any{parent + ".osmDatabaseKey": value},
					})
				}
				if strings.HasPrefix(path, "api.") {
					cases = append(cases, schemaCase{name: "unknown diagnostics property", changes: map[string]any{parent + ".databaseSecrets": map[string]any{"name": "fixture-reader-secret", "key": "fixtureReaderKey"}}})
				}
				for _, tc := range cases {
					tc.name = prefix + "/" + tc.name
					if tc.changes == nil {
						tc.changes = map[string]any{}
					}
					tc.changes["osm.useReadReplicasOnly"] = readerOnly
					tc.changes["api.coverageDiagnostics.enabled"] = diagnostics
					tests = append(tests, tc)
				}
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var values map[string]any
			if err := yaml.Unmarshal(rawValues, &values); err != nil {
				t.Fatal(err)
			}
			for path, value := range tc.changes {
				parts := strings.Split(path, ".")
				target := values
				for _, part := range parts[:len(parts)-1] {
					next, ok := target[part].(map[string]any)
					if !ok {
						t.Fatalf("cannot override %s: %s is not an object in default values", path, part)
					}
					target = next
				}
				target[parts[len(parts)-1]] = value
			}
			for _, path := range tc.remove {
				parts := strings.Split(path, ".")
				target := values
				for _, part := range parts[:len(parts)-1] {
					next, ok := target[part].(map[string]any)
					if !ok {
						t.Fatalf("cannot remove %s: %s is not an object in default values", path, part)
					}
					target = next
				}
				delete(target, parts[len(parts)-1])
			}
			if err := schema.Validate(values); (err == nil) != tc.valid {
				t.Fatalf("schema valid=%t, want %t: %v", err == nil, tc.valid, err)
			}
		})
	}
	if _, err := exec.LookPath("helm"); err == nil {
		output, err := exec.Command("helm", "template", "osm-replica-conflict", chartDir, "--set", "osm.useReadReplicasOnly=true,osm.autoAddRegions=true").CombinedOutput()
		if err == nil {
			t.Fatalf("Helm accepted reader-only auto-add conflict: %s", output)
		}
	}
}

func TestOSMReadReplicaDNSArtifact(t *testing.T) {
	raw, err := os.ReadFile(repoPath("deploy/dev/otel-dns.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var configMap struct {
		Kind     string
		Metadata struct{ Name, Namespace string }
		Data     map[string]string
	}
	if err := yaml.Unmarshal(raw, &configMap); err != nil {
		t.Fatal(err)
	}
	if configMap.Kind != "ConfigMap" || configMap.Metadata.Name != "coredns-custom" || configMap.Metadata.Namespace != "kube-system" {
		t.Fatal("unexpected DNS ConfigMap identity")
	}
	want := map[string]string{
		"workouts-otel.server":  "otel-gateway-collector.observability.svc.cluster.local:1053",
		"postgresql-osm.server": "postgresql-postgresql-headless.postgresql.svc.cluster.local:1053",
	}
	if len(configMap.Data) != len(want) {
		t.Fatalf("expected exactly two custom DNS data keys, got %d", len(configMap.Data))
	}
	header := regexp.MustCompile(`(?m)^[ \t]*([^#{} \t\r\n]+)[ \t]*\{[ \t]*$`)
	forward := regexp.MustCompile(`(?m)^[ \t]*forward[ \t]+[^\r\n]+`)
	hostDNS := regexp.MustCompile(`^[ \t]*forward[ \t]+\.[ \t]+10\.43\.0\.10[ \t]*$`)
	privateZone := regexp.MustCompile(`(?m)^[ \t]*(postgresql\.svc\.cluster\.local|postgresql\.postgresql\.svc\.cluster\.local)(:[0-9]+)?[ \t]*\{`)
	for key, server := range want {
		corefile, ok := configMap.Data[key]
		if !ok {
			t.Errorf("missing custom DNS data key %s", key)
			continue
		}
		if privateZone.MatchString(corefile) {
			t.Errorf("%s: forwarding must not shadow the virtual PostgreSQL namespace or private database service", key)
		}
		blocks := header.FindAllStringSubmatch(corefile, -1)
		if len(blocks) != 1 || blocks[0][1] != server {
			t.Errorf("%s: expected exactly one server block for %s", key, server)
		}
		forwards := forward.FindAllString(corefile, -1)
		if len(forwards) != 1 || !hostDNS.MatchString(forwards[0]) {
			t.Errorf("%s: expected exactly one forward directive to host DNS 10.43.0.10", key)
		}
	}
}
