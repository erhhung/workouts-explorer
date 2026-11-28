package osm

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StorageUsageNode struct {
	Pod                    string    `json:"pod"`
	Namespace              string    `json:"namespace"`
	Role                   string    `json:"role"`
	PVCs                   []string  `json:"pvcs"`
	CapacityBytes          int64     `json:"capacityBytes"`
	RequestedCapacityBytes *int64    `json:"requestedCapacityBytes"`
	AvailableBytes         int64     `json:"availableBytes"`
	UsedBytes              int64     `json:"usedBytes"`
	ApplicationBytes       *int64    `json:"applicationBytes"`
	SampledAt              time.Time `json:"sampledAt"`
	Issue                  string    `json:"issue,omitempty"`
}
type StorageUsage struct {
	Configured bool               `json:"configured"`
	CheckedAt  time.Time          `json:"checkedAt"`
	Databases  []string           `json:"databases"`
	Nodes      []StorageUsageNode `json:"nodes"`
}

// StorageUsageReader observes the cluster anchored by a configured PostgreSQL PVC.
// It does not apply processing admission policy or require a database connection.
type StorageUsageReader struct {
	PrometheusURL  string
	SelectorLabels map[string]string
	Databases      []string
	HTTPClient     *http.Client
	Now            func() time.Time
	mu             sync.Mutex
	cached         *StorageUsage
}

func (r *StorageUsageReader) Configured() bool {
	return r != nil && r.PrometheusURL != "" && r.SelectorLabels["namespace"] != "" && r.SelectorLabels["persistentvolumeclaim"] != "" && len(r.Databases) > 0
}

func metricSelector(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+strconv.Quote(labels[key]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
func regexSelector(labels map[string]string, key string, values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, regexp.QuoteMeta(value))
	}
	selector := metricSelector(labels)
	return strings.TrimSuffix(selector, "}") + "," + key + "=~" + strconv.Quote(strings.Join(parts, "|")) + "}"
}

func (r *StorageUsageReader) Read(ctx context.Context) (StorageUsage, error) {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	empty := StorageUsage{Configured: r.Configured(), CheckedAt: now, Databases: r.Databases, Nodes: []StorageUsageNode{}}
	if !empty.Configured {
		return empty, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if r.cached != nil && now.Sub(r.cached.CheckedAt) >= 0 && now.Sub(r.cached.CheckedAt) < 15*time.Second {
		return *r.cached, nil
	}
	value, err := r.read(ctx, now)
	if err != nil {
		return empty, err
	}
	r.cached = &value
	return value, nil
}

func (r *StorageUsageReader) read(ctx context.Context, now time.Time) (StorageUsage, error) {
	result := StorageUsage{Configured: true, CheckedAt: now, Databases: append([]string(nil), r.Databases...), Nodes: []StorageUsageNode{}}
	base, err := url.Parse(r.PrometheusURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.Fragment != "" || len(r.Databases) > 16 {
		return result, errors.New("invalid storage metrics configuration")
	}
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	p := PrometheusStoragePreflight{HTTPClient: client, MaximumAge: DefaultMaximumAge, RetryDelays: []time.Duration{}}
	get := func(metric, selector string) ([]prometheusSample, error) {
		samples, err := p.queryWithTimestamps(ctx, base, metric+selector, "", now)
		if err != nil {
			return nil, err
		}
		if len(samples) > 1024 {
			return nil, errors.New("too many storage metric samples")
		}
		for _, sample := range samples {
			if sample.Value >= float64(math.MaxInt64) {
				return nil, errors.New("storage metric exceeds supported size")
			}
			if sample.Metric["__name__"] != metric {
				return nil, errors.New("unexpected storage metric")
			}
			if err = p.validateSample(sample, now); err != nil {
				return nil, err
			}
		}
		return samples, nil
	}
	namespace := r.SelectorLabels["namespace"]
	anchors, err := get("kube_pod_spec_volumes_persistentvolumeclaims_info", metricSelector(map[string]string{"namespace": namespace, "persistentvolumeclaim": r.SelectorLabels["persistentvolumeclaim"]}))
	if err != nil {
		return result, err
	}
	anchorPod := ""
	for _, sample := range anchors {
		if sample.Value != 1 || sample.Metric["namespace"] != namespace || sample.Metric["persistentvolumeclaim"] != r.SelectorLabels["persistentvolumeclaim"] || sample.Metric["pod"] == "" {
			return result, errors.New("invalid anchor PVC mapping")
		}
		if anchorPod != "" && anchorPod != sample.Metric["pod"] {
			return result, errors.New("ambiguous anchor PVC mapping")
		}
		anchorPod = sample.Metric["pod"]
	}
	if anchorPod == "" {
		return result, errors.New("PostgreSQL PVC mapping unavailable")
	}
	anchorRoles, err := get("pg_replication_is_replica", metricSelector(map[string]string{"namespace": namespace, "pod": anchorPod}))
	if err != nil {
		return result, err
	}
	if len(anchorRoles) != 1 || anchorRoles[0].Metric["service"] == "" || anchorRoles[0].Metric["namespace"] != namespace || anchorRoles[0].Metric["pod"] != anchorPod {
		return result, errors.New("PostgreSQL exporter mapping unavailable or ambiguous")
	}
	scope := map[string]string{"namespace": namespace, "service": anchorRoles[0].Metric["service"]}
	if job := anchorRoles[0].Metric["job"]; job != "" {
		scope["job"] = job
	}
	roles, err := get("pg_replication_is_replica", metricSelector(scope))
	if err != nil {
		return result, err
	}
	nodes := map[string]*StorageUsageNode{}
	pods := []string{}
	primary := 0
	for _, sample := range roles {
		pod := sample.Metric["pod"]
		if pod == "" || sample.Metric["namespace"] != namespace || sample.Metric["service"] != scope["service"] || (scope["job"] != "" && sample.Metric["job"] != scope["job"]) || (sample.Value != 0 && sample.Value != 1) || nodes[pod] != nil {
			return result, errors.New("invalid or ambiguous PostgreSQL roles")
		}
		role := "replica"
		if sample.Value == 0 {
			role = "primary"
			primary++
		}
		nodes[pod] = &StorageUsageNode{Pod: pod, Namespace: namespace, Role: role, PVCs: []string{}, SampledAt: sample.Timestamp}
		pods = append(pods, pod)
	}
	if primary != 1 || len(nodes) > 32 {
		return result, errors.New("PostgreSQL primary is unavailable or ambiguous")
	}
	mappings, err := get("kube_pod_spec_volumes_persistentvolumeclaims_info", regexSelector(map[string]string{"namespace": namespace}, "pod", pods))
	if err != nil {
		return result, err
	}
	pvcOwner := map[string]string{}
	for _, sample := range mappings {
		pod, pvc := sample.Metric["pod"], sample.Metric["persistentvolumeclaim"]
		node := nodes[pod]
		if node == nil || pvc == "" || sample.Value != 1 || sample.Metric["namespace"] != namespace {
			return result, errors.New("invalid PostgreSQL volume mapping")
		}
		if owner, exists := pvcOwner[pvc]; exists {
			if owner != pod {
				return result, errors.New("shared PostgreSQL volume mapping is ambiguous")
			}
			continue
		}
		pvcOwner[pvc] = pod
		node.PVCs = append(node.PVCs, pvc)
		if sample.Timestamp.Before(node.SampledAt) {
			node.SampledAt = sample.Timestamp
		}
	}
	pvcs := make([]string, 0, len(pvcOwner))
	for pvc := range pvcOwner {
		pvcs = append(pvcs, pvc)
	}
	if len(pvcs) == 0 {
		return result, errors.New("PostgreSQL volumes are unavailable")
	}
	for _, metric := range []string{"kubelet_volume_stats_capacity_bytes", "kubelet_volume_stats_available_bytes", "kubelet_volume_stats_used_bytes"} {
		values, err := get(metric, regexSelector(map[string]string{"namespace": namespace}, "persistentvolumeclaim", pvcs))
		if err != nil {
			return result, err
		}
		seen := map[string]bool{}
		for _, sample := range values {
			pvc := sample.Metric["persistentvolumeclaim"]
			node := nodes[pvcOwner[pvc]]
			if node == nil || seen[pvc] || sample.Metric["namespace"] != namespace {
				return result, errors.New("invalid or ambiguous volume metrics")
			}
			seen[pvc] = true
			value := int64(sample.Value)
			if node.CapacityBytes > math.MaxInt64-value || node.AvailableBytes > math.MaxInt64-value || node.UsedBytes > math.MaxInt64-value {
				return result, errors.New("combined storage size exceeds supported size")
			}
			switch metric {
			case "kubelet_volume_stats_capacity_bytes":
				node.CapacityBytes += value
			case "kubelet_volume_stats_available_bytes":
				node.AvailableBytes += value
			default:
				node.UsedBytes += value
			}
			if sample.Timestamp.Before(node.SampledAt) {
				node.SampledAt = sample.Timestamp
			}
		}
		if len(seen) != len(pvcs) {
			return result, errors.New("volume metrics are missing")
		}
	}
	requestedPVCs := map[string]int64{}
	invalidRequests := map[string]bool{}
	requests, requestErr := get("kube_persistentvolumeclaim_resource_requests_storage_bytes", regexSelector(map[string]string{"namespace": namespace}, "persistentvolumeclaim", pvcs))
	if requestErr == nil {
		for _, sample := range requests {
			pvc := sample.Metric["persistentvolumeclaim"]
			if _, known := pvcOwner[pvc]; !known || sample.Metric["namespace"] != namespace {
				requestedPVCs = nil
				break
			}
			if _, duplicate := requestedPVCs[pvc]; duplicate || sample.Value <= 0 {
				invalidRequests[pvc] = true
			}
			requestedPVCs[pvc] = int64(sample.Value)
		}
	}
	sizes, err := get("pg_database_size_bytes", regexSelector(scope, "datname", r.Databases))
	sizesAvailable := err == nil
	databaseSizes := map[string]map[string]int64{}
	if sizesAvailable {
		for _, sample := range sizes {
			pod, db := sample.Metric["pod"], sample.Metric["datname"]
			node := nodes[pod]
			wanted := false
			for _, name := range r.Databases {
				wanted = wanted || name == db
			}
			if node == nil || !wanted || sample.Metric["namespace"] != namespace || sample.Metric["service"] != scope["service"] {
				return result, errors.New("invalid database size metric")
			}
			if databaseSizes[pod] == nil {
				databaseSizes[pod] = map[string]int64{}
			}
			if _, duplicate := databaseSizes[pod][db]; duplicate {
				return result, errors.New("ambiguous database size metric")
			}
			databaseSizes[pod][db] = int64(sample.Value)
			if sample.Timestamp.Before(node.SampledAt) {
				node.SampledAt = sample.Timestamp
			}
		}
	}
	for _, node := range nodes {
		if len(node.PVCs) == 0 || node.CapacityBytes <= 0 || node.AvailableBytes > node.CapacityBytes || node.UsedBytes > node.CapacityBytes {
			return result, errors.New("invalid PostgreSQL volume usage")
		}
		var requestedTotal int64
		completeRequest := true
		for _, pvc := range node.PVCs {
			size, found := requestedPVCs[pvc]
			if !found || invalidRequests[pvc] || requestedTotal > math.MaxInt64-size {
				completeRequest = false
				break
			}
			requestedTotal += size
		}
		if completeRequest {
			node.RequestedCapacityBytes = &requestedTotal
		}
		if sizesAvailable && len(databaseSizes[node.Pod]) == len(r.Databases) {
			var size int64
			for _, value := range databaseSizes[node.Pod] {
				if size > math.MaxInt64-value {
					return result, errors.New("combined database size exceeds supported size")
				}
				size += value
			}
			if size <= node.UsedBytes {
				node.ApplicationBytes = &size
			} else {
				node.Issue = "Database and volume measurements are not yet consistent."
			}
		}
		if node.ApplicationBytes == nil && node.Issue == "" {
			node.Issue = "Per-database storage measurements are unavailable."
		}
		sort.Strings(node.PVCs)
		result.Nodes = append(result.Nodes, *node)
	}
	sort.Slice(result.Nodes, func(i, j int) bool {
		a, b := result.Nodes[i], result.Nodes[j]
		if a.Role != b.Role {
			return a.Role == "primary"
		}
		return a.Pod < b.Pod
	})
	return result, nil
}
