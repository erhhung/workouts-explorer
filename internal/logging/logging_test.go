package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestConfiguredFormat(t *testing.T) {
	for _, test := range []struct {
		name, format string
		json         bool
	}{
		{name: "default", json: true},
		{name: "json", format: "json", json: true},
		{name: "text", format: "text"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			newLogger("workouts-test", test.format, &output).Info("ready", "port", 8080)
			if test.json {
				var record map[string]any
				if err := json.Unmarshal(output.Bytes(), &record); err != nil {
					t.Fatalf("decode JSON log: %v", err)
				}
				if record["msg"] != "ready" || record["service"] != "workouts-test" || record["port"] != float64(8080) {
					t.Fatalf("record = %#v", record)
				}
				return
			}
			line := output.String()
			for _, field := range []string{"level=INFO", "msg=ready", "service=workouts-test", "port=8080"} {
				if !strings.Contains(line, field) {
					t.Fatalf("text log %q missing %q", line, field)
				}
			}
		})
	}
}

func TestNewInstallsDefaultLogger(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	t.Setenv("LOG_FORMAT", "text")
	logger := New("workouts-test")
	if slog.Default() != logger {
		t.Fatal("configured logger was not installed as slog default")
	}
}
