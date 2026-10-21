package rclonesource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	appleCanary    = "PRIVATE-APPLE-CANARY@example.invalid"
	trustCanary    = "PRIVATE-TRUST-CANARY"
	cookieCanary   = "PRIVATE-COOKIE-CANARY"
	contentCanary  = "PRIVATE-CONTENT-CANARY"
	helperMarker   = "SOURCE_ADAPTER_TEST_CHILD"
	helperScenario = "SOURCE_ADAPTER_TEST_SCENARIO"
)

// Use the test executable as the CLI, without requiring configurable process
// arguments in production. These hooks exist only in the test binary.
func init() {
	if os.Getenv(helperMarker) != "1" {
		return
	}
	if os.Getenv("SOURCE_ADAPTER_TEST_GRANDCHILD") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	helperMain()
	os.Exit(0)
}

func helperFail() { fmt.Fprintln(os.Stderr, "private fake-child assertion failed"); os.Exit(91) }

func helperMain() {
	args := os.Args[1:]
	flag := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	configPath := flag("--config")
	dir := filepath.Dir(configPath)
	if !filepath.IsAbs(configPath) || filepath.Base(configPath) != configName {
		helperFail()
	}
	for _, pair := range [][2]string{{"--log-level", "ERROR"}, {"--stats", "0"}, {"--retries", "2"}, {"--low-level-retries", "2"}, {"--retries-sleep", "1s"}, {"--cache-dir", filepath.Join(dir, "cache")}} {
		if flag(pair[0]) != pair[1] {
			helperFail()
		}
	}
	for _, canary := range []string{appleCanary, trustCanary, cookieCanary, testConfig().Password} {
		if strings.Contains(strings.Join(args, " "), canary) {
			helperFail()
		}
	}
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(strings.ToUpper(key), "RCLONE") {
			helperFail()
		}
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"} {
		if !strings.HasPrefix(os.Getenv(key), dir+string(os.PathSeparator)) {
			helperFail()
		}
	}
	for _, path := range []string{dir, flag("--cache-dir"), os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_CACHE_HOME")} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			helperFail()
		}
	}
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		helperFail()
	}
	stdin, err := io.ReadAll(io.LimitReader(os.Stdin, 1))
	if err != nil || len(stdin) != 0 {
		helperFail()
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		helperFail()
	}
	values, ok := parseConfig(bytes.NewReader(data))
	if !ok || !strings.EqualFold(values["apple_id"], appleCanary) || values["service"] != "drive" || values["trust_token"] != trustCanary {
		helperFail()
	}
	var command string
	for _, arg := range args {
		if arg == "version" || arg == "lsjson" || arg == "cat" {
			command = arg
			break
		}
	}
	scenario := os.Getenv(helperScenario)
	if command == "version" {
		if scenario == "version-hang" {
			if os.WriteFile(filepath.Join(filepath.Dir(dir), "fake-version-started"), []byte("started"), 0600) != nil {
				helperFail()
			}
			for {
				time.Sleep(time.Hour)
			}
		}
		if scenario == "wrong-version-cleanup-denied" {
			if os.Chmod(dir, 0500) != nil {
				helperFail()
			}
			fmt.Println("rclone v1.75.1")
			return
		}
		if scenario == "wrong-version" {
			fmt.Println("rclone v1.75.1")
			return
		}
		fmt.Println("rclone v1.75.2\n- fake platform")
		return
	}
	if err = os.WriteFile(filepath.Join(dir, "fake-started"), []byte("started"), 0600); err != nil {
		helperFail()
	}
	if scenario == "hang" || scenario == "orphan" {
		if scenario == "orphan" {
			child := exec.Command(os.Args[0])
			child.Env = append(os.Environ(), "SOURCE_ADAPTER_TEST_GRANDCHILD=1")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if child.Start() != nil {
				helperFail()
			}
			if os.WriteFile(filepath.Join(dir, "fake-grandchild"), []byte(fmt.Sprint(child.Process.Pid)), 0600) != nil {
				helperFail()
			}
			os.Exit(0)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if scenario == "refresh" || scenario == "refresh-fail" || scenario == "refresh-auth-fail" || scenario == "empty-cookies" || scenario == "change-trust" || scenario == "extra-section" || scenario == "auth-session" {
		updated := strings.Replace(string(data), "cookies = sid="+cookieCanary, "cookies = sid=REFRESHED-COOKIE-CANARY", 1)
		switch scenario {
		case "empty-cookies":
			updated = strings.Replace(updated, "sid=REFRESHED-COOKIE-CANARY", "", 1)
		case "change-trust":
			updated = strings.Replace(updated, trustCanary, "CHANGED-TRUST-CANARY", 1)
		case "extra-section":
			updated += "\n[other]\ntype = local\n"
		case "auth-session":
			updated += "_auth_session = \n"
		}
		if os.WriteFile(configPath, []byte(updated), 0600) != nil {
			helperFail()
		}
		if scenario == "refresh-fail" {
			fmt.Fprintln(os.Stderr, "unavailable "+contentCanary)
			os.Exit(1)
		}
		if scenario == "refresh-auth-fail" {
			fmt.Fprintln(os.Stderr, "authSRPComplete: sign in failed: incorrect username or password")
			os.Exit(1)
		}
	}
	if scenario == "private-error" {
		for i := 0; i < 2000; i++ {
			fmt.Fprintln(os.Stderr, appleCanary, trustCanary, cookieCanary, contentCanary, "https://private.invalid/?secret=canary")
		}
		os.Exit(1)
	}
	if scenario == "stdout-limit" {
		_, _ = io.CopyN(os.Stdout, endlessByte{}, maxOutput+1)
		return
	}
	remote := args[len(args)-1]
	if command == "lsjson" {
		if !strings.Contains(strings.Join(args, " "), "--no-mimetype") {
			helperFail()
		}
		if slices.Contains(args, "--stat") {
			if remote == "source:exports" {
				if scenario == "not-directory" {
					fmt.Println(`{"Name":"","Path":"","IsDir":false}`)
					return
				}
				fmt.Println(`{"Name":"","Path":"","IsDir":true}`)
				return
			}
			name := filepath.Base(remote)
			item := helperItem(name)
			if scenario == "pre-mutation" {
				item["Size"] = 6
			}
			if scenario == "post-mutation" {
				if _, err := os.Stat(filepath.Join(dir, "fake-cat")); err == nil {
					item["ID"] = "changed-id"
				}
			}
			if scenario == "stat-missing" {
				fmt.Println("null")
				return
			}
			_ = json.NewEncoder(os.Stdout).Encode(item)
			return
		}
		if flag("--max-depth") != "1" || strings.Contains(strings.Join(args, " "), "--recursive") {
			helperFail()
		}
		if scenario == "bad-json" {
			fmt.Print(`[{"Name":"../private","Path":"../private","IsDir":false}]`)
			return
		}
		_ = json.NewEncoder(os.Stdout).Encode([]any{helperItem("HealthAutoExport-2026-01-02.json"), helperItem("HealthAutoExport-2026-01-01.json")})
		return
	}
	if command == "cat" {
		if remote != "source:exports" || flag("--max-depth") != "1" || flag("--transfers") != "1" || !eligibleName(strings.TrimPrefix(flag("--include"), "/")) || !strings.HasPrefix(flag("--include"), "/") {
			helperFail()
		}
		if os.WriteFile(filepath.Join(dir, "fake-cat"), []byte("cat"), 0600) != nil {
			helperFail()
		}
		switch scenario {
		case "cat-hang":
			fmt.Print("hello")
			for {
				time.Sleep(time.Hour)
			}
		case "cat-short":
			fmt.Print("hell")
		case "cat-short-cleanup-denied":
			fmt.Print("hell")
			if os.Chmod(dir, 0500) != nil {
				helperFail()
			}
		case "cat-large":
			_, _ = io.CopyN(os.Stdout, endlessByte{}, maxFile+1)
		case "max-file":
			_, _ = io.CopyN(os.Stdout, endlessByte{}, maxFile)
		case "max-file-over":
			_, _ = io.CopyN(os.Stdout, endlessByte{}, maxFile+1)
		case "cat-long":
			fmt.Print("hello!")
		case "cat-exit":
			fmt.Print("hello")
			fmt.Fprintln(os.Stderr, contentCanary)
			os.Exit(1)
		case "cat-empty", "zero-file":
			return
		default:
			fmt.Print("hello")
		}
		return
	}
	helperFail()
}

func helperItem(name string) map[string]any {
	item := map[string]any{"Name": name, "Path": name, "IsDir": false, "ID": "id-" + name, "Size": 5, "ModTime": "2026-01-03T00:00:00Z", "MimeType": "ignored", "NativeFutureField": map[string]any{"foo": "ignored"}}
	switch os.Getenv(helperScenario) {
	case "max-file", "max-file-over":
		item["Size"] = maxFile
	case "zero-file":
		item["Size"] = 0
	}
	return item
}

type endlessByte struct{}

func (endlessByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
