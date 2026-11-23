package osmcontroller

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Job struct {
	Spec     json.RawMessage `json:"spec"`
	Metadata struct {
		Name   string            `json:"name"`
		UID    string            `json:"uid"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Active    int `json:"active"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"status"`
}
type Jobs interface {
	Get(context.Context, string) (*Job, error)
	Create(context.Context, map[string]any) (*Job, error)
	Delete(context.Context, string, string) error
	PodsStopped(context.Context, string) (bool, error)
	Failure(context.Context, string, string) (ExecutionFailure, error)
}

type ExecutionFailure struct {
	Reason      string
	ExitCode    int
	MemoryLimit string
}

func (failure ExecutionFailure) Summary() string {
	switch failure.Reason {
	case "OOMKilled":
		limit := failure.MemoryLimit
		if limit == "" {
			limit = "configured"
		}
		return fmt.Sprintf("Updater exceeded its %s memory limit (OOMKilled, exit %d).", limit, failure.ExitCode)
	case "Evicted":
		return "Kubernetes evicted the updater pod. Check node memory and scratch capacity."
	case "":
		return ""
	default:
		return fmt.Sprintf("Updater container exited with code %d. Review the updater pod logs for this job.", failure.ExitCode)
	}
}

type Kubernetes struct {
	client                 *http.Client
	base, token, namespace string
	owner                  map[string]any
}

func (k *Kubernetes) DeploymentOwner(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("controller deployment name is unavailable")
	}
	var deployment struct{ Metadata struct{ Name, UID string } }
	status, err := k.request(ctx, "GET", "/apis/apps/v1/namespaces/"+url.PathEscape(k.namespace)+"/deployments/"+url.PathEscape(name), nil, &deployment)
	if err != nil {
		return err
	}
	if status != 200 || deployment.Metadata.Name != name || deployment.Metadata.UID == "" {
		return errors.New("controller deployment identity is unavailable")
	}
	k.owner = map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": name, "uid": deployment.Metadata.UID, "controller": true, "blockOwnerDeletion": false}
	return nil
}

func InCluster(namespace string) (*Kubernetes, error) {
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, err
	}
	certificate, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, errors.New("Kubernetes CA unavailable")
	}
	return &Kubernetes{client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}, base: "https://" + os.Getenv("KUBERNETES_SERVICE_HOST") + ":" + os.Getenv("KUBERNETES_SERVICE_PORT"), token: strings.TrimSpace(string(token)), namespace: namespace}, nil
}
func (k *Kubernetes) request(ctx context.Context, method, path string, body any, target any) (int, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, k.base+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+k.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := k.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == 404 || response.StatusCode == 409 {
		return response.StatusCode, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("Kubernetes request status %d", response.StatusCode)
	}
	if target != nil {
		err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target)
	}
	return response.StatusCode, err
}
func (k *Kubernetes) path(name string) string {
	p := "/apis/batch/v1/namespaces/" + url.PathEscape(k.namespace) + "/jobs"
	if name != "" {
		p += "/" + url.PathEscape(name)
	}
	return p
}
func (k *Kubernetes) Get(ctx context.Context, name string) (*Job, error) {
	var job Job
	status, err := k.request(ctx, "GET", k.path(name), nil, &job)
	if status == 404 {
		return nil, nil
	}
	return &job, err
}
func (k *Kubernetes) Create(ctx context.Context, spec map[string]any) (*Job, error) {
	if k.owner != nil {
		spec["metadata"].(map[string]any)["ownerReferences"] = []any{k.owner}
	}
	var job Job
	status, err := k.request(ctx, "POST", k.path(""), spec, &job)
	if status == 409 {
		return k.Get(ctx, spec["metadata"].(map[string]any)["name"].(string))
	}
	return &job, err
}
func (k *Kubernetes) Delete(ctx context.Context, name, uid string) error {
	_, err := k.request(ctx, "DELETE", k.path(name), map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground", "gracePeriodSeconds": 120, "preconditions": map[string]string{"uid": uid}}, nil)
	return err
}
func (k *Kubernetes) PodsStopped(ctx context.Context, id string) (bool, error) {
	var result struct {
		Items []struct{ Status struct{ Phase string } } `json:"items"`
	}
	_, err := k.request(ctx, "GET", "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/pods?labelSelector="+url.QueryEscape("workouts-explorer/region-update="+id), nil, &result)
	if err != nil {
		return false, err
	}
	for _, pod := range result.Items {
		if pod.Status.Phase != "Succeeded" && pod.Status.Phase != "Failed" {
			return false, nil
		}
	}
	return true, nil
}

func (k *Kubernetes) Failure(ctx context.Context, id, uid string) (ExecutionFailure, error) {
	var result struct {
		Items []struct {
			Metadata struct{ OwnerReferences []struct{ UID, Kind string } }
			Spec     struct {
				Containers []struct {
					Name      string
					Resources struct{ Limits map[string]string }
				}
			}
			Status struct {
				Reason            string
				ContainerStatuses []struct {
					Name  string
					State struct {
						Terminated *struct {
							Reason   string
							ExitCode int
						}
					}
				}
			}
		}
	}
	_, err := k.request(ctx, "GET", "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/pods?labelSelector="+url.QueryEscape("workouts-explorer/region-update="+id), nil, &result)
	if err != nil {
		return ExecutionFailure{}, err
	}
	for _, pod := range result.Items {
		owned := false
		for _, owner := range pod.Metadata.OwnerReferences {
			if owner.Kind == "Job" && owner.UID == uid {
				owned = true
			}
		}
		if !owned {
			continue
		}
		if pod.Status.Reason == "Evicted" {
			return ExecutionFailure{Reason: "Evicted"}, nil
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "osm-update" || status.State.Terminated == nil || status.State.Terminated.ExitCode == 0 {
				continue
			}
			failure := ExecutionFailure{Reason: status.State.Terminated.Reason, ExitCode: status.State.Terminated.ExitCode}
			for _, container := range pod.Spec.Containers {
				if container.Name == "osm-update" {
					failure.MemoryLimit = container.Resources.Limits["memory"]
				}
			}
			return failure, nil
		}
	}
	return ExecutionFailure{}, nil
}
