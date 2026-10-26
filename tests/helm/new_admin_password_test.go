package helm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestHelmAdminPasswordConfiguration(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, setting := range []string{"default", "false", "true"} {
		for _, bootstrap := range []bool{false, true} {
			for _, local := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/bootstrap=%t/local=%t", setting, bootstrap, local), func(t *testing.T) {
					const adminSecret = "fixture-bootstrap-credentials"
					minimum := "12"
					args := []string{"template", "admin-password-test", chartDir,
						"--set", "api.bootstrap.enabled=" + strconv.FormatBool(bootstrap),
						"--set", "api.localDevelopment=" + strconv.FormatBool(local),
						"--set-string", "api.bootstrap.secretName=" + adminSecret}
					if setting != "default" {
						minimum = "16"
						args = append(args, "--set", "api.allowAdminPassword="+setting,
							"--set", "api.passwordMinimumLength="+minimum)
					}
					output, err := exec.Command("helm", args...).CombinedOutput()
					if err != nil {
						t.Fatalf("render: %v\n%s", err, output)
					}
					want := "false"
					if setting == "true" {
						want = "true"
					}
					seenConfig, seenAPI, seenBootstrap := false, false, false
					decoder := yaml.NewDecoder(bytes.NewReader(output))
					for {
						var document yaml.Node
						if err := decoder.Decode(&document); err == io.EOF {
							break
						} else if err != nil {
							t.Fatal(err)
						}
						var item workload
						if err := document.Decode(&item); err != nil {
							t.Fatal(err)
						}
						if item.Kind == "ConfigMap" && item.Metadata.Name == "workouts-explorer-api" {
							seenConfig = true
							if item.Data["ALLOW_ADMIN_PASSWORD"] != want || item.Data["PASSWORD_MINIMUM_LENGTH"] != minimum {
								t.Errorf("unexpected API password configuration: %v", item.Data)
							}
							var config struct{ Data yaml.Node }
							if err := document.Decode(&config); err != nil {
								t.Fatal(err)
							}
							for i := 0; i < len(config.Data.Content); i += 2 {
								if config.Data.Content[i].Value != "ALLOW_ADMIN_PASSWORD" {
									continue
								}
								value := config.Data.Content[i+1]
								if value.Tag != "!!str" || value.Style != yaml.DoubleQuotedStyle {
									t.Errorf("ALLOW_ADMIN_PASSWORD must be a double-quoted string: %+v", value)
								}
								if i < 2 || config.Data.Content[i-2].Value != "PASSWORD_MINIMUM_LENGTH" {
									t.Error("ALLOW_ADMIN_PASSWORD must immediately follow PASSWORD_MINIMUM_LENGTH")
								}
							}
						}
						if item.Kind == "Job" && item.Spec.Template.Metadata.Labels["app.kubernetes.io/component"] == "bootstrap-admin" {
							seenBootstrap = true
						}
						if item.Kind == "Deployment" && item.Metadata.Name == "workouts-explorer-api" {
							seenAPI = true
							// Inspect the whole pod, including envFrom, init containers and projected volumes.
							raw, err := yaml.Marshal(&document)
							if err != nil {
								t.Fatal(err)
							}
							if bytes.Contains(raw, []byte(adminSecret)) || bytes.Contains(raw, []byte("BOOTSTRAP_")) {
								t.Error("runtime API must not reference bootstrap credentials")
							}
						}
					}
					if !seenConfig || !seenAPI || seenBootstrap != bootstrap {
						t.Errorf("unexpected resources: config=%t api=%t bootstrap=%t", seenConfig, seenAPI, seenBootstrap)
					}
				})
			}
		}
	}
	for _, tc := range []struct{ flag, setting, field string }{
		{"--set-string", "api.allowAdminPassword=true", "allowAdminPassword"},
		{"--set-string", "api.allowAdminPassword=false", "allowAdminPassword"},
		{"--set", "api.allowAdminPassword=1", "allowAdminPassword"},
		{"--set", "api.passwordMinimumLength=11", "passwordMinimumLength"},
		{"--set", "api.passwordMinimumLength=65", "passwordMinimumLength"},
		{"--set-string", "api.passwordMinimumLength=12", "passwordMinimumLength"},
	} {
		t.Run("invalid/"+tc.setting, func(t *testing.T) {
			output, err := exec.Command("helm", "template", "invalid-admin-password-test", chartDir,
				"--set", "api.allowAdminPassword=true", tc.flag, tc.setting).CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte(tc.field)) {
				t.Fatalf("expected schema rejection for %s: err=%v\n%s", tc.setting, err, output)
			}
		})
	}
}

func TestHelmAdminPasswordDefaultsAndSchema(t *testing.T) {
	rawValues, err := os.ReadFile(chartPath("values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(rawValues, &values); err != nil {
		t.Fatal(err)
	}
	api := values["api"].(map[string]any)
	if api["allowAdminPassword"] != false {
		t.Fatal("api.allowAdminPassword must default to boolean false")
	}
	var ordered struct {
		API yaml.Node `yaml:"api"`
	}
	if err := yaml.Unmarshal(rawValues, &ordered); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(ordered.API.Content); i += 2 {
		if ordered.API.Content[i].Value == "allowAdminPassword" && (i < 2 || ordered.API.Content[i-2].Value != "passwordMinimumLength") {
			t.Error("api.allowAdminPassword must immediately follow api.passwordMinimumLength")
		}
	}
	rawSchema, err := os.ReadFile(chartPath("values.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(rawSchema, &document); err != nil {
		t.Fatal(err)
	}
	apiSchema := document["properties"].(map[string]any)["api"].(map[string]any)
	property := apiSchema["properties"].(map[string]any)["allowAdminPassword"].(map[string]any)
	if property["type"] != "boolean" || property["default"] != false || !slices.Contains(apiSchema["required"].([]any), any("allowAdminPassword")) {
		t.Fatal("schema must require allowAdminPassword as a boolean with default false")
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("values.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{false, true, "false", "true", 0, 1, nil} {
		api["allowAdminPassword"] = value
		_, valid := value.(bool)
		if err := schema.Validate(values); (err == nil) != valid {
			t.Errorf("schema value=%#v: valid=%t want %t: %v", value, err == nil, valid, err)
		}
	}
	delete(api, "allowAdminPassword")
	if err := schema.Validate(values); err == nil {
		t.Error("schema must reject a missing allowAdminPassword in structured values")
	}
}

func TestHelmAdminPasswordDocumentation(t *testing.T) {
	raw, err := os.ReadFile(chartPath("README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "| `allowAdminPassword`<br>_(bool)_ |") {
			continue
		}
		for _, warning := range []string{"WARNING", "administrator access to ALL user accounts", "judiciously ONLY for development/debugging", "NEVER in production", "| `false` |"} {
			if !strings.Contains(line, warning) {
				t.Errorf("generated allowAdminPassword row lacks %q: %s", warning, line)
			}
		}
		return
	}
	t.Fatal("generated README must document api.allowAdminPassword and its danger warning")
}
