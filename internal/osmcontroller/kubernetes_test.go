package osmcontroller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFailureUsesOnlyOwnedPodAndRetainsBoundedOOMDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/workouts/pods" || r.URL.Query().Get("labelSelector") != "workouts-explorer/region-update=request-id" {
			t.Errorf("unscoped pod lookup: %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"items":[
      {"metadata":{"ownerReferences":[{"kind":"Job","uid":"foreign"}]},"status":{"reason":"Evicted"}},
      {"metadata":{"ownerReferences":[{"kind":"Job","uid":"owned"}]},"spec":{"containers":[{"name":"osm-update","resources":{"limits":{"memory":"1Gi"}}}]},
      "status":{"containerStatuses":[{"name":"osm-update","state":{"terminated":{"reason":"OOMKilled","exitCode":137,"message":"postgres://user:secret@private/database"}}}]}}
    ]}`)
	}))
	defer server.Close()
	client := Kubernetes{client: server.Client(), base: server.URL, namespace: "workouts"}
	failure, err := client.Failure(context.Background(), "request-id", "owned")
	if err != nil {
		t.Fatal(err)
	}
	summary := failure.Summary()
	if summary != "Updater exceeded its 1Gi memory limit (OOMKilled, exit 137)." {
		t.Fatal(summary)
	}
	if strings.Contains(summary, "secret") {
		t.Fatal("raw pod diagnostics leaked")
	}
	foreign, err := client.Failure(context.Background(), "request-id", "missing")
	if err != nil || foreign.Summary() != "" {
		t.Fatalf("foreign pod accepted: %+v %v", foreign, err)
	}
}

func TestJobsUseStableDeploymentOwnership(t *testing.T) {
	const name, uid = "workouts-explorer-osm-controller", "stable-deployment-uid"
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /apis/apps/v1/namespaces/workouts/deployments/" + name:
			_, _ = io.WriteString(w, `{"metadata":{"name":"`+name+`","uid":"`+uid+`"}}`)
		case "POST /apis/batch/v1/namespaces/workouts/jobs":
			var spec map[string]any
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				t.Fatal(err)
			}
			owners := spec["metadata"].(map[string]any)["ownerReferences"].([]any)
			if len(owners) != 1 {
				t.Fatal("missing unique deployment owner")
			}
			owner := owners[0].(map[string]any)
			if owner["kind"] != "Deployment" || owner["apiVersion"] != "apps/v1" || owner["name"] != name || owner["uid"] != uid || owner["controller"] != true || owner["blockOwnerDeletion"] != false {
				t.Fatal("rollout-unsafe or overprivileged ownership", owner)
			}
			created++
			_, _ = io.WriteString(w, `{"metadata":{"name":"job","uid":"job-uid"}}`)
		default:
			t.Errorf("unexpected or unscoped Kubernetes request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	k := Kubernetes{client: server.Client(), base: server.URL, namespace: "workouts"}
	if err := k.DeploymentOwner(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"updater", "cleanup"} {
		if _, err := k.Create(context.Background(), map[string]any{"metadata": map[string]any{"name": job}, "spec": map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	if created != 2 {
		t.Fatal("updater and cleanup did not receive deployment ownership")
	}
	if err := k.DeploymentOwner(context.Background(), ""); err == nil {
		t.Fatal("missing controller identity accepted")
	}
}
