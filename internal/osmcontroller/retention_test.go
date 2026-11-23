package osmcontroller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestPodsStoppedAllowsRetainedTerminalPodsButBlocksLiveOrUnknownPods(t *testing.T) {
	for _, test := range []struct {
		name, pods string
		stopped    bool
	}{
		{"absent", `[]`, true},
		{"retained terminal", `[{"status":{"phase":"Succeeded"}},{"status":{"phase":"Failed"}}]`, true},
		{"pending", `[{"status":{"phase":"Pending"}}]`, false},
		{"running alongside retained", `[{"status":{"phase":"Succeeded"}},{"status":{"phase":"Running"}}]`, false},
		{"unknown", `[{"status":{"phase":"Unknown"}}]`, false},
		{"missing phase", `[{"status":{}}]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/namespaces/workouts/pods" || r.URL.Query().Get("labelSelector") != "workouts-explorer/region-update=request" {
					t.Errorf("unscoped pod lookup: %s", r.URL)
				}
				_, _ = io.WriteString(w, `{"items":`+test.pods+`}`)
			}))
			defer server.Close()
			client := Kubernetes{client: server.Client(), base: server.URL, namespace: "workouts"}
			stopped, err := client.PodsStopped(context.Background(), "request")
			if err != nil || stopped != test.stopped {
				t.Fatalf("stopped=%v want=%v: %v", stopped, test.stopped, err)
			}
		})
	}
}

func TestCompletedJobRetentionTemplateForAllExecutionModes(t *testing.T) {
	for _, mode := range []string{"run", "cleanup", "delete"} {
		for _, seconds := range []float64{0, 123, 21600} {
			controller := Controller{Template: controllerTemplate()}
			controller.Template["spec"].(map[string]any)["ttlSecondsAfterFinished"] = seconds
			spec, err := controller.specification(execution{ID: uuid.New(), Region: "geofabrik:monaco", Image: controller.container()["image"].(string), Token: "fixture"}, "retention-fixture", mode)
			if err != nil || spec["spec"].(map[string]any)["ttlSecondsAfterFinished"] != seconds {
				t.Fatalf("mode=%s ttl=%v: %v", mode, seconds, err)
			}
		}
	}
	for _, invalid := range []any{-1, 1.5, 2147483648, "6h"} {
		controller := Controller{Template: controllerTemplate()}
		controller.Template["spec"].(map[string]any)["ttlSecondsAfterFinished"] = invalid
		if err := controller.Validate(); err == nil {
			t.Fatalf("invalid retention accepted: %v", invalid)
		}
	}
}
