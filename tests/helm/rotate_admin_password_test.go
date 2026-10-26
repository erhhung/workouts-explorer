package helm_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/oasdiff/yaml3"
)

const rotationPassword = "b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7b7"

const rotationInitCommand = `cp /secret/password /private/password && actual=$(sha256sum /private/password) && [ "${actual%% *}" = "$EXPECTED_PASSWORD_SHA256" ] && chmod 0400 /private/password && chown 65532:65532 /private/password`

var rotationPasswordSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(rotationPassword)))

type rotationFixture struct {
	Values                           map[string]any
	Secret                           map[string]any
	Context, Namespace, Release      string
	Minimum, Scenario, Status, Image string
}

func rotationWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// The script's children are this test binary, never a real kubectl or openssl.
// Only helm template and jq are forwarded, locally, to exercise the chart/JSON.
func TestRotateAdminPasswordChild(t *testing.T) {
	if os.Getenv("ROTATION_CHILD") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	name, args := filepath.Base(args[0]), args[1:]
	root := os.Getenv("ROTATION_FIXTURE")
	read := func(path string, value any) {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || json.Unmarshal(raw, value) != nil {
			panic("cannot read test fixture")
		}
	}
	var f rotationFixture
	read("fixture.json", &f)
	log, err := os.OpenFile(filepath.Join(root, "commands"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(log).Encode(append([]string{name}, args...)); err != nil {
		panic(err)
	}
	log.Close()
	fail := func() {
		fmt.Fprintln(os.Stderr, "private-config-marker old-password-marker "+rotationPassword+" "+rotationPasswordSHA256)
		os.Exit(23)
	}
	if err := filepath.WalkDir(os.Getenv("TMPDIR"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if d.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			return fmt.Errorf("unsafe private file permissions: %s", path)
		}
		return nil
	}); err != nil {
		panic(err)
	}
	for _, arg := range args {
		if strings.Contains(arg, rotationPassword) || strings.Contains(arg, rotationPasswordSHA256) || strings.Contains(arg, "old-password-marker") || strings.Contains(arg, "private-config-marker") {
			panic("credential in process argv")
		}
	}
	emit := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	flag := func(key string) string {
		i := slices.Index(args, key)
		if i < 0 || i+1 == len(args) {
			panic("missing flag " + key)
		}
		return args[i+1]
	}
	switch name {
	case "jq":
		cmd := exec.Command(os.Getenv("ROTATION_REAL_JQ"), args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
	case "helm":
		if !slices.Equal(args[:4], []string{"--kube-context", f.Context, "--namespace", f.Namespace}) {
			panic("helm context/namespace not explicit")
		}
		args = args[4:]
		switch args[0] {
		case "status":
			emit(map[string]any{"name": f.Release, "namespace": f.Namespace, "info": map[string]any{"status": f.Status}})
		case "get":
			if !slices.Equal(args, []string{"get", "values", f.Release, "--all", "-o", "json"}) {
				panic("unexpected values request")
			}
			emit(f.Values)
		case "template":
			if args[1] != f.Release || flag("--show-only") != "templates/bootstrap-job.yaml" {
				panic("not rendering only bootstrap hook")
			}
			chart, err := filepath.Abs(chartDir)
			if err != nil || args[2] != chart || !strings.HasPrefix(flag("--values"), os.Getenv("TMPDIR")+"/") {
				panic("not using the requested chart and private installed values")
			}
			want := []string{"template", f.Release, chart, "--values", flag("--values"), "--show-only", "templates/bootstrap-job.yaml",
				"--set", "api.bootstrap.enabled=true", "--set", "api.bootstrap.rotateExistingPassword=true", "--set", args[len(args)-1]}
			if !slices.Equal(args, want) || !strings.HasPrefix(args[len(args)-1], "api.passwordMinimumLength=") {
				panic("unexpected template overrides")
			}
			var saved map[string]any
			savedRaw, err := os.ReadFile(flag("--values"))
			if err != nil || json.Unmarshal(savedRaw, &saved) != nil || !reflect.DeepEqual(saved, f.Values) {
				panic("installed values were modified")
			}
			cmd := exec.Command(os.Getenv("ROTATION_REAL_HELM"), args...)
			raw, err := cmd.CombinedOutput()
			if err != nil {
				fail()
			}
			var job map[string]any
			if err := yaml.Unmarshal(raw, &job); err != nil {
				panic(err)
			}
			rotationWrite(t, filepath.Join(root, "base-job.json"), job)
			pod := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			container := pod["containers"].([]any)[0].(map[string]any)
			switch f.Scenario {
			case "command":
				container["command"] = []any{"/bin/sh"}
			case "image":
				container["image"] = "unpublished:latest"
			case "database-ref":
				container["env"].([]any)[1].(map[string]any)["valueFrom"] = map[string]any{"secretKeyRef": map[string]any{"name": "wrong-role", "key": "url"}}
			case "sidecar":
				pod["containers"] = append(pod["containers"].([]any), container)
			case "list":
				emit(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{job}})
				os.Exit(0)
			}
			emit(job)
			if f.Scenario == "extra-resource" {
				fmt.Println("---\nkind: ConfigMap\napiVersion: v1\nmetadata:\n  name: forbidden")
			}
		default:
			panic("forbidden helm command")
		}
	case "openssl":
		if len(args) == 3 && slices.Equal(args[:2], []string{"dgst", "-sha256"}) {
			raw, err := os.ReadFile(args[2])
			if err != nil || string(raw) != rotationPassword || !strings.HasPrefix(args[2], os.Getenv("TMPDIR")+"/") {
				panic("checksum must read only the private generated password file")
			}
			fmt.Printf("SHA2-256(%s)= %x\n", args[2], sha256.Sum256(raw))
			os.Exit(0)
		}
		if !slices.Equal(args, []string{"rand", "-hex", "32"}) {
			panic("wrong entropy request")
		}
		switch f.Scenario {
		case "entropy-short":
			fmt.Println("abcd")
		case "entropy-nonhex":
			fmt.Println(strings.Repeat("z", 64))
		default:
			fmt.Println(rotationPassword)
		}
	case "kubectl":
		if !slices.Equal(args[:5], []string{"--context", f.Context, "--namespace", f.Namespace, "--request-timeout=10s"}) {
			panic("kubectl context/namespace not explicit")
		}
		args = args[5:]
		switch args[0] {
		case "get":
			switch args[1] {
			case "configmap":
				if args[2] != "workouts-explorer-api" {
					panic("wrong ConfigMap name")
				}
				emit(map[string]any{"data": map[string]any{"PASSWORD_MINIMUM_LENGTH": f.Minimum}})
			case "deployment":
				emit(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": f.Image}}}}}})
			case "secret":
				b := f.Values["api"].(map[string]any)["bootstrap"].(map[string]any)
				if args[2] == b["secretName"] {
					if _, err := os.Stat(filepath.Join(root, "patched.json")); err == nil {
						if _, err := os.Stat(filepath.Join(root, "job-complete.json")); err != nil {
							panic("confirmation read must follow Job completion")
						}
						read("live-secret.json", &f.Secret)
						switch f.Scenario {
						case "final-read-failed":
							fail()
						case "final-password-change":
							f.Secret["data"].(map[string]any)[b["passwordKey"].(string)] = "b3ZlcndyaXR0ZW4="
						case "final-otherkey-change":
							f.Secret["data"].(map[string]any)["unrelated"] = "b3ZlcndyaXR0ZW4="
						case "final-uid-change":
							f.Secret["metadata"].(map[string]any)["uid"] = "recreated-secret"
						case "final-rv-change":
							f.Secret["metadata"].(map[string]any)["resourceVersion"] = "44"
						}
					}
					emit(f.Secret)
				} else if args[2] == b["databaseSecret"].(map[string]any)["name"] {
					if f.Scenario == "missing-db-key" {
						emit(map[string]any{"data": map[string]any{}})
						os.Exit(0)
					}
					emit(map[string]any{"data": map[string]any{b["databaseSecret"].(map[string]any)["key"].(string): "ZGItcHJpdmF0ZS1tYXJrZXI="}})
				} else {
					panic("unexpected Secret read")
				}
			case "job":
				if f.Scenario == "job-read-failed" {
					fail()
				}
				condition := "Complete"
				_, guardErr := os.Stat(filepath.Join(root, "guard-failed.json"))
				if f.Scenario == "job-failed" || guardErr == nil {
					condition = "Failed"
				}
				if f.Scenario == "timeout" {
					emit(map[string]any{})
				} else {
					if condition == "Complete" {
						rotationWrite(t, filepath.Join(root, "job-complete.json"), true)
					}
					emit(map[string]any{"status": map[string]any{"conditions": []any{map[string]any{"type": condition, "status": "True"}}}})
				}
			default:
				panic("unexpected get")
			}
		case "create":
			if slices.Contains(args, "--dry-run=client") {
				raw, err := os.ReadFile(flag("-f"))
				if err != nil || !slices.Contains(args, "--validate=false") {
					panic("invalid client conversion")
				}
				decoder := yaml.NewDecoder(bytes.NewReader(raw))
				for {
					var doc map[string]any
					if err := decoder.Decode(&doc); err == io.EOF {
						break
					} else if err != nil {
						panic(err)
					}
					emit(doc)
				}
			} else {
				if f.Scenario == "create-failed" {
					fail()
				}
				if _, err := os.Stat(filepath.Join(root, "patched.json")); err != nil {
					panic("Job created before Secret patch")
				}
				raw, err := os.ReadFile(flag("-f"))
				if err != nil || os.WriteFile(filepath.Join(root, "created.json"), raw, 0600) != nil {
					panic("cannot record created Job")
				}
				var created, base map[string]any
				if err := json.Unmarshal(raw, &created); err != nil {
					panic(err)
				}
				read("base-job.json", &base)
				pod := created["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				basePod := base["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				init := pod["initContainers"].([]any)[0].(map[string]any)
				baseInit := basePod["initContainers"].([]any)[0].(map[string]any)
				baseInit["command"], baseInit["env"] = init["command"], init["env"]
				if !reflect.DeepEqual(pod, basePod) {
					panic("copy guard changed the native bootstrap container or other pod settings")
				}
				mounted := rotationPassword
				if f.Scenario == "mounted-old" {
					mounted = "old-password-marker"
				} else if f.Scenario == "mounted-mismatch" {
					mounted = strings.Repeat("c", 64)
				} else if f.Scenario == "post-cas-overwrite" {
					var live map[string]any
					read("live-secret.json", &live)
					b := f.Values["api"].(map[string]any)["bootstrap"].(map[string]any)
					decoded, err := base64.StdEncoding.DecodeString(live["data"].(map[string]any)[b["passwordKey"].(string)].(string))
					if err != nil {
						panic(err)
					}
					mounted = string(decoded)
				}
				if !rotationRunInitGuard(t, root, init, mounted) {
					rotationWrite(t, filepath.Join(root, "guard-failed.json"), true)
				}
			}
		case "patch":
			if f.Scenario == "conflict" {
				fail()
			}
			if !slices.Contains(args, "--type=json") || flag("-o") != "json" || args[1] != "secret" || args[2] != f.Secret["metadata"].(map[string]any)["name"] {
				panic("unexpected patch target/type")
			}
			raw, err := os.ReadFile(flag("--patch-file"))
			if err != nil {
				panic(err)
			}
			var patch []struct{ Op, Path, Value string }
			if err := json.Unmarshal(raw, &patch); err != nil || len(patch) != 2 || patch[0].Op != "test" || patch[0].Path != "/metadata/resourceVersion" || patch[0].Value != "42" || patch[1].Op != "replace" {
				panic("unsafe JSON patch")
			}
			key := strings.TrimPrefix(patch[1].Path, "/data/")
			f.Secret["data"].(map[string]any)[key] = patch[1].Value
			f.Secret["metadata"].(map[string]any)["resourceVersion"] = "43"
			rotationWrite(t, filepath.Join(root, "patched.json"), f.Secret)
			rotationWrite(t, filepath.Join(root, "live-secret.json"), f.Secret)
			if f.Scenario == "post-cas-overwrite" {
				var live map[string]any
				read("live-secret.json", &live)
				live["metadata"].(map[string]any)["resourceVersion"] = "44"
				live["data"].(map[string]any)[key] = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("c", 64)))
				rotationWrite(t, filepath.Join(root, "live-secret.json"), live)
			}
			switch f.Scenario {
			case "patch-read-failed":
				fail()
			case "patch-response-password":
				f.Secret["data"].(map[string]any)[key] = "b3ZlcndyaXR0ZW4="
			case "patch-response-otherkey":
				f.Secret["data"].(map[string]any)["unrelated"] = "b3ZlcndyaXR0ZW4="
			case "patch-response-uid":
				f.Secret["metadata"].(map[string]any)["uid"] = "recreated-secret"
			case "patch-response-rv-empty":
				delete(f.Secret["metadata"].(map[string]any), "resourceVersion")
			case "patch-response-rv-unchanged":
				f.Secret["metadata"].(map[string]any)["resourceVersion"] = "42"
			case "patch-response-malformed":
				fmt.Println("not-json")
				os.Exit(0)
			}
			emit(f.Secret)
		default:
			panic("forbidden kubectl mutation")
		}
	default:
		panic("unexpected child")
	}
	os.Exit(0)
}

func rotationRunInitGuard(t *testing.T, root string, init map[string]any, mounted string) bool {
	t.Helper()
	if !reflect.DeepEqual(init["command"], []any{"/bin/sh", "-ec", rotationInitCommand}) ||
		!reflect.DeepEqual(init["env"], []any{map[string]any{"name": "EXPECTED_PASSWORD_SHA256", "value": rotationPasswordSHA256}}) {
		t.Fatal("init must have exactly the copy guard and one expected checksum environment entry")
	}
	bin := filepath.Join(root, "guard-bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Exercise the real shell/copy/checksum. Only ownership changes are stubbed,
	// so this also runs unprivileged without assigning test files to another UID.
	if err := os.WriteFile(filepath.Join(bin, "chown"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source, private, marker := filepath.Join(root, "mounted-password"), filepath.Join(root, "copied-password"), filepath.Join(root, "native-bootstrap-marker")
	if err := os.WriteFile(source, []byte(mounted), 0600); err != nil {
		t.Fatal(err)
	}
	body := strings.NewReplacer("/secret/password", `"$SOURCE_PASSWORD_FILE"`, "/private/password", `"$PRIVATE_PASSWORD_FILE"`).Replace(rotationInitCommand)
	cmd := exec.Command("/bin/sh", "-ec", body+` && : > "$NATIVE_BOOTSTRAP_MARKER"`)
	cmd.Env = append(os.Environ(), "EXPECTED_PASSWORD_SHA256="+rotationPasswordSHA256, "SOURCE_PASSWORD_FILE="+source,
		"PRIVATE_PASSWORD_FILE="+private, "NATIVE_BOOTSTRAP_MARKER="+marker, "PATH="+bin+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if len(output) != 0 {
		t.Fatal("copy guard must not print password bytes or actual/expected checksums")
	}
	_, markerErr := os.Stat(marker)
	if (err == nil) != (markerErr == nil) {
		t.Fatal("native bootstrap marker must run only after a successful copy guard")
	}
	return err == nil
}

func TestRotateAdminPasswordInitGuard(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is required")
	}
	for _, tc := range []struct {
		name, mounted string
		allowed       bool
	}{
		{"generated", rotationPassword, true},
		{"stale-old", "old-password-marker", false},
		{"other-64-hex", strings.Repeat("c", 64), false},
		{"trailing-newline", rotationPassword + "\n", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init := map[string]any{"command": []any{"/bin/sh", "-ec", rotationInitCommand},
				"env": []any{map[string]any{"name": "EXPECTED_PASSWORD_SHA256", "value": rotationPasswordSHA256}}}
			if rotationRunInitGuard(t, t.TempDir(), init, tc.mounted) != tc.allowed {
				t.Fatal("copy guard accepted incorrect bytes or rejected the generated password")
			}
		})
	}
}

func TestRotateAdminPassword(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is required for real hook rendering")
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is required")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is required for local guard execution")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, scenario, minimum, status string
		installed                       any
		options                         []string
		custom, success, patched        bool
	}{
		{name: "help", options: []string{"--help"}, success: true},
		{name: "dry-run", options: []string{"--dry-run"}, success: true},
		{name: "dry-run-with-yes", options: []string{"--dry-run", "--yes"}, success: true},
		{name: "yes-required"},
		{name: "context-required", options: []string{"no-context", "--yes"}},
		{name: "unknown-option", options: []string{"--password-minimum", "12", "--yes"}},
		{name: "missing-option-value", options: []string{"--context"}},
		{name: "bad-timeout", options: []string{"--yes", "--timeout", "0s"}},
		{name: "not-deployed", status: "pending-upgrade", options: []string{"--yes"}},
		{name: "installed-short", installed: 11, options: []string{"--yes"}},
		{name: "installed-long", installed: 65, options: []string{"--yes"}},
		{name: "installed-string", installed: "12", options: []string{"--yes"}},
		{name: "installed-nondigit", installed: "invalid", options: []string{"--yes"}},
		{name: "installed-fractional", installed: 12.5, options: []string{"--yes"}},
		{name: "installed-missing", scenario: "installed-missing", options: []string{"--yes"}},
		{name: "config-short", minimum: "11", options: []string{"--yes"}},
		{name: "config-long", minimum: "65", options: []string{"--yes"}},
		{name: "config-nondigit", minimum: "invalid", options: []string{"--yes"}},
		{name: "config-missing", minimum: "missing", options: []string{"--yes"}},
		{name: "success", options: []string{"--yes"}, success: true, patched: true},
		{name: "trace-disabled", options: []string{"--yes"}, success: true, patched: true},
		{name: "defaults", options: []string{"--yes"}, success: true, patched: true},
		{name: "explicit-chart", options: []string{"--yes"}, success: true, patched: true},
		{name: "custom-secret", options: []string{"--yes"}, custom: true, success: true, patched: true},
		{name: "higher-config", minimum: "64", options: []string{"--yes"}, success: true, patched: true},
		{name: "higher-installed", minimum: "12", installed: 32, options: []string{"--yes"}, success: true, patched: true},
		{name: "conflict", scenario: "conflict", options: []string{"--yes"}},
		{name: "extra-resource", scenario: "extra-resource", options: []string{"--yes"}},
		{name: "list", scenario: "list", options: []string{"--yes"}},
		{name: "wrong-command", scenario: "command", options: []string{"--yes"}},
		{name: "wrong-image", scenario: "image", options: []string{"--yes"}},
		{name: "wrong-database-ref", scenario: "database-ref", options: []string{"--yes"}},
		{name: "sidecar", scenario: "sidecar", options: []string{"--yes"}},
		{name: "deployment-image-drift", scenario: "deployment-image-drift", options: []string{"--yes"}},
		{name: "missing-password", scenario: "missing-password", options: []string{"--yes"}},
		{name: "missing-version", scenario: "missing-version", options: []string{"--yes"}},
		{name: "missing-uid", scenario: "missing-uid", options: []string{"--yes"}},
		{name: "immutable-secret", scenario: "immutable-secret", options: []string{"--yes"}},
		{name: "missing-db-key", scenario: "missing-db-key", options: []string{"--yes"}},
		{name: "short-entropy", scenario: "entropy-short", options: []string{"--yes"}},
		{name: "nonhex-entropy", scenario: "entropy-nonhex", options: []string{"--yes"}},
		{name: "job-failed", scenario: "job-failed", options: []string{"--yes"}, patched: true},
		{name: "create-failed", scenario: "create-failed", options: []string{"--yes"}, patched: true},
		{name: "job-read-failed", scenario: "job-read-failed", options: []string{"--yes"}, patched: true},
		{name: "timeout", scenario: "timeout", options: []string{"--yes", "--timeout", "1s"}, patched: true},
		{name: "mounted-old", scenario: "mounted-old", options: []string{"--yes"}, patched: true},
		{name: "mounted-mismatch", scenario: "mounted-mismatch", options: []string{"--yes"}, patched: true},
		{name: "post-cas-overwrite", scenario: "post-cas-overwrite", options: []string{"--yes"}, patched: true},
		{name: "final-password-change", scenario: "final-password-change", options: []string{"--yes"}, patched: true},
		{name: "final-otherkey-change", scenario: "final-otherkey-change", options: []string{"--yes"}, patched: true},
		{name: "final-uid-change", scenario: "final-uid-change", options: []string{"--yes"}, patched: true},
		{name: "final-rv-change", scenario: "final-rv-change", options: []string{"--yes"}, patched: true},
		{name: "final-read-failed", scenario: "final-read-failed", options: []string{"--yes"}, patched: true},
		{name: "patch-response-password", scenario: "patch-response-password", options: []string{"--yes"}, patched: true},
		{name: "patch-response-otherkey", scenario: "patch-response-otherkey", options: []string{"--yes"}, patched: true},
		{name: "patch-response-uid", scenario: "patch-response-uid", options: []string{"--yes"}, patched: true},
		{name: "patch-response-rv-empty", scenario: "patch-response-rv-empty", options: []string{"--yes"}, patched: true},
		{name: "patch-response-rv-unchanged", scenario: "patch-response-rv-unchanged", options: []string{"--yes"}, patched: true},
		{name: "patch-response-malformed", scenario: "patch-response-malformed", options: []string{"--yes"}, patched: true},
		{name: "patch-read-failed", scenario: "patch-read-failed", options: []string{"--yes"}, patched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin, tmp := filepath.Join(root, "bin"), filepath.Join(root, "private")
			for _, dir := range []string{bin, tmp} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"helm", "kubectl", "openssl", "jq"} {
				wrapper := "#!/bin/bash\nexec \"$ROTATION_BINARY\" -test.run=^TestRotateAdminPasswordChild$ -- \"$0\" \"$@\"\n"
				if err := os.WriteFile(filepath.Join(bin, name), []byte(wrapper), 0700); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(chartPath("values.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := yaml.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			api := values["api"].(map[string]any)
			api["smtp"].(map[string]any)["username"] = "private-config-marker"
			if tc.installed != nil {
				api["passwordMinimumLength"] = tc.installed
			}
			if tc.scenario == "installed-missing" {
				delete(api, "passwordMinimumLength")
			}
			b := api["bootstrap"].(map[string]any)
			if tc.custom {
				b["secretName"], b["passwordKey"], b["usernameKey"], b["emailKey"] = "custom-admin", "admin.pw", "login", "mail"
				b["databaseSecret"] = map[string]any{"name": "custom-bootstrap-db", "key": "role-url"}
			}
			image := values["image"].(map[string]any)
			image["registry"], image["tag"] = "installed.example", "published"
			image["digests"] = map[string]any{"workouts-api": "sha256:" + strings.Repeat("a", 64)}
			f := rotationFixture{Values: values, Context: "explicit-test-context", Namespace: "custom-namespace", Release: "installed-release", Minimum: "16", Status: "deployed", Scenario: tc.scenario,
				Image: "installed.example/workouts-api:published@sha256:" + strings.Repeat("a", 64)}
			if tc.minimum != "" {
				f.Minimum = tc.minimum
			}
			if tc.minimum == "missing" {
				f.Minimum = ""
			}
			if tc.status != "" {
				f.Status = tc.status
			}
			if tc.name == "defaults" {
				f.Namespace, f.Release = "workouts-explorer", "workouts-dev"
			}
			if tc.scenario == "deployment-image-drift" {
				f.Image = "different:latest"
			}
			f.Secret = map[string]any{"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
				"metadata": map[string]any{"name": b["secretName"], "namespace": f.Namespace, "uid": "original-secret", "resourceVersion": "42", "annotations": map[string]any{"keep": "unchanged"}},
				"data":     map[string]any{b["passwordKey"].(string): base64.StdEncoding.EncodeToString([]byte("old-password-marker")), b["usernameKey"].(string): "YWRtaW4=", b["emailKey"].(string): "YWRtaW5AZXhhbXBsZS5jb20=", "unrelated": "a2VlcA=="}}
			switch tc.scenario {
			case "missing-password":
				delete(f.Secret["data"].(map[string]any), b["passwordKey"].(string))
			case "missing-version":
				delete(f.Secret["metadata"].(map[string]any), "resourceVersion")
			case "missing-uid":
				delete(f.Secret["metadata"].(map[string]any), "uid")
			case "immutable-secret":
				f.Secret["immutable"] = true
			}
			rotationWrite(t, filepath.Join(root, "fixture.json"), f)
			args := []string{repoPath("scripts/rotate-admin-password.sh"), "--context", f.Context, "--namespace", f.Namespace, "--release", f.Release}
			if tc.name == "defaults" {
				args = args[:3]
			}
			if tc.name == "explicit-chart" {
				chart, err := filepath.Abs(chartDir)
				if err != nil {
					t.Fatal(err)
				}
				args = append(args, "--chart", chart)
			}
			if slices.Contains(tc.options, "no-context") {
				args = args[:1]
				tc.options = tc.options[1:]
			}
			args = append(args, tc.options...)
			if tc.name == "trace-disabled" {
				args = append([]string{"-x"}, args...)
			}
			cmd := exec.Command("bash", args...)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TMPDIR="+tmp,
				"ROTATION_CHILD=1", "ROTATION_BINARY="+binary, "ROTATION_FIXTURE="+root, "ROTATION_REAL_HELM="+helm, "ROTATION_REAL_JQ="+jq)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("success=%t, err=%v\n%s", tc.success, err, output)
			}
			commands, _ := os.ReadFile(filepath.Join(root, "commands"))
			for _, secret := range []string{rotationPassword, rotationPasswordSHA256, "old-password-marker", "private-config-marker", base64.StdEncoding.EncodeToString([]byte(rotationPassword)), base64.StdEncoding.EncodeToString([]byte("old-password-marker")), "ZGItcHJpdmF0ZS1tYXJrZXI="} {
				if bytes.Contains(output, []byte(secret)) || bytes.Contains(commands, []byte(secret)) {
					t.Fatal("credential leaked to output or process argv")
				}
			}
			var calls [][]string
			decoder := json.NewDecoder(bytes.NewReader(commands))
			for {
				var call []string
				if err := decoder.Decode(&call); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				calls = append(calls, call)
			}
			switch tc.name {
			case "help", "yes-required", "context-required", "unknown-option", "missing-option-value", "bad-timeout":
				if len(calls) != 0 {
					t.Fatal("argument validation must precede cluster and credential commands")
				}
			}
			patches, creates, entropy, checksums, secretReads := 0, 0, 0, 0, 0
			for _, call := range calls {
				if call[0] == "openssl" && call[1] == "rand" {
					entropy++
				}
				if call[0] == "openssl" && call[1] == "dgst" {
					checksums++
				}
				if i := slices.Index(call, "get"); call[0] == "kubectl" && i >= 0 && call[i+1] == "secret" && call[i+2] == b["secretName"] {
					secretReads++
				}
				for _, forbidden := range []string{"upgrade", "install", "apply", "delete", "rollout", "logs", "--from-literal", "api.allowAdminPassword=true"} {
					if slices.Contains(call, forbidden) {
						t.Fatalf("forbidden operation: %v", call)
					}
				}
				if call[0] == "kubectl" && slices.Contains(call, "patch") {
					patches++
				}
				if call[0] == "kubectl" && slices.Contains(call, "create") && !slices.Contains(call, "--dry-run=client") {
					creates++
					if patches != 1 {
						t.Fatal("Job create did not follow exactly one Secret patch")
					}
				}
			}
			patched, patchErr := os.ReadFile(filepath.Join(root, "patched.json"))
			if (patchErr == nil) != tc.patched || creates > 1 || patches > 1 || (!tc.patched && creates != 0) {
				t.Fatalf("unexpected mutation: patched=%t patches=%d creates=%d\n%s", patchErr == nil, patches, creates, output)
			}
			if tc.patched {
				wantCreates := 1
				if strings.HasPrefix(tc.scenario, "patch-response-") || tc.scenario == "patch-read-failed" {
					wantCreates = 0
				}
				if len(rotationPassword) != 64 || entropy != 1 || checksums != 1 || patches != 1 || creates != wantCreates {
					t.Fatal("rotation must generate one 64-character password, patch once, then create one Job")
				}
				var got map[string]any
				if err := json.Unmarshal(patched, &got); err != nil {
					t.Fatal(err)
				}
				f.Secret["data"].(map[string]any)[b["passwordKey"].(string)] = base64.StdEncoding.EncodeToString([]byte(rotationPassword))
				f.Secret["metadata"].(map[string]any)["resourceVersion"] = "43"
				if !reflect.DeepEqual(got, f.Secret) {
					t.Fatal("rotation changed other Secret data or metadata")
				}
				if !tc.success && !strings.Contains(string(output), "Secret updated; database rotation not confirmed. Inspect this job before retrying.") {
					t.Fatalf("missing post-patch safety warning: %s", output)
				}
			}
			if !tc.patched && tc.scenario != "conflict" && patches != 0 {
				t.Fatal("preflight failure or dry-run attempted Secret patch")
			}
			if slices.Contains(tc.options, "--dry-run") && (entropy != 0 || checksums != 0 || patches != 0 || creates != 0) {
				t.Fatal("dry-run generated password material or mutated cluster")
			}
			if !(tc.success && tc.patched) && strings.Contains(string(output), "Database rotation confirmed") {
				t.Fatal("reported database rotation without successful native Job completion")
			}
			if tc.success && tc.patched {
				if secretReads != 2 {
					t.Fatal("success requires initial and post-completion Secret reads")
				}
				if !strings.Contains(string(output), "Database rotation confirmed by successful native Job") {
					t.Fatal("missing native Job success confirmation")
				}
				raw, err := os.ReadFile(filepath.Join(root, "created.json"))
				if err != nil {
					t.Fatal(err)
				}
				var job map[string]any
				if err := json.Unmarshal(raw, &job); err != nil {
					t.Fatal(err)
				}
				metadata := job["metadata"].(map[string]any)
				if len(metadata["name"].(string)) > 63 || metadata["namespace"] != f.Namespace || len(metadata["annotations"].(map[string]any)) != 0 {
					t.Fatal("invalid cloned Job metadata")
				}
				spec := job["spec"].(map[string]any)
				if spec["activeDeadlineSeconds"] != float64(300) || spec["ttlSecondsAfterFinished"] != float64(3600) {
					t.Fatal("missing bounded deadline or TTL")
				}
				container := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
				minimum := 16
				if tc.name == "higher-config" {
					minimum = 64
				} else if tc.name == "higher-installed" {
					minimum = 32
				}
				if !slices.Contains(container["args"].([]any), any(fmt.Sprintf("--password-minimum=%d", minimum))) || container["image"] != f.Image {
					t.Fatal("did not preserve effective minimum and installed image digest")
				}
			}
			if tc.scenario == "mounted-old" || tc.scenario == "mounted-mismatch" || tc.scenario == "post-cas-overwrite" {
				if _, err := os.Stat(filepath.Join(root, "guard-failed.json")); err != nil {
					t.Fatal("stale or overwritten mounted bytes must fail the actual init guard")
				}
				if _, err := os.Stat(filepath.Join(root, "native-bootstrap-marker")); !os.IsNotExist(err) || secretReads != 1 {
					t.Fatal("failed copy guard must block native bootstrap and completion confirmation")
				}
			}
			if strings.HasPrefix(tc.scenario, "final-") {
				if _, err := os.Stat(filepath.Join(root, "native-bootstrap-marker")); err != nil || secretReads != 2 {
					t.Fatal("final Secret confirmation regression must exercise a completed native Job")
				}
			}
			if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
				t.Fatal("private temporary files not cleaned up")
			}
		})
	}
}

func TestRotateAdminPasswordClientConversion(t *testing.T) {
	for _, dependency := range []string{"helm", "kubectl"} {
		if _, err := exec.LookPath(dependency); err != nil {
			t.Skip(dependency + " is required")
		}
	}
	// Real kubectl may discover APIs even in client dry-run mode. Its only server
	// here is this loopback fixture, with no real kubeconfig or credentials.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("client conversion attempted %s %s", r.Method, r.URL.Path)
			http.Error(w, "read-only fixture", http.StatusForbidden)
			return
		}
		var response any
		switch r.URL.Path {
		case "/api":
			response = map[string]any{"kind": "APIVersions", "apiVersion": "v1", "versions": []string{"v1"}}
		case "/apis":
			response = map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": []any{map[string]any{
				"name": "batch", "versions": []any{map[string]any{"groupVersion": "batch/v1", "version": "v1"}},
				"preferredVersion": map[string]any{"groupVersion": "batch/v1", "version": "v1"}}}}
		case "/api/v1":
			response = map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "v1", "resources": []any{}}
		case "/apis/batch/v1":
			response = map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "batch/v1", "resources": []any{
				map[string]any{"name": "jobs", "singularName": "job", "namespaced": true, "kind": "Job", "verbs": []string{"get", "create"}}}}
		default:
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	root := t.TempDir()
	config := filepath.Join(root, "kubeconfig.json")
	rotationWrite(t, config, map[string]any{"apiVersion": "v1", "kind": "Config",
		"clusters": []any{map[string]any{"name": "fixture", "cluster": map[string]any{"server": server.URL}}},
		"contexts": []any{map[string]any{"name": "only-loopback", "context": map[string]any{"cluster": "fixture"}}}})
	raw, err := exec.Command("helm", "template", "conversion-test", chartDir,
		"--show-only", "templates/bootstrap-job.yaml", "--set", "api.bootstrap.enabled=true",
		"--set", "api.bootstrap.rotateExistingPassword=true").CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, raw)
	}
	input := filepath.Join(root, "job.yaml")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := yaml.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	converted, err := exec.Command("kubectl", "--kubeconfig", config, "--context", "only-loopback", "--namespace", "fixture",
		"--request-timeout=3s", "--cache-dir", filepath.Join(root, "cache"), "create", "--dry-run=client", "--validate=false", "-f", input, "-o", "json").CombinedOutput()
	if err != nil {
		t.Fatalf("client conversion: %v\n%s", err, converted)
	}
	var got map[string]any
	if err := json.Unmarshal(converted, &got); err != nil {
		t.Fatal(err)
	}
	want["metadata"].(map[string]any)["namespace"] = "fixture"
	// Normalize YAML integer types before comparing against JSON.
	normalized, _ := json.Marshal(want)
	if err := json.Unmarshal(normalized, &want); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("kubectl changed the native hook beyond namespace:\n%s", converted)
	}
}
