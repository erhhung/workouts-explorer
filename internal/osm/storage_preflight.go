package osm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	DefaultPrometheusURL = "https://prometheus-stack-prometheus.monitoring.svc.cluster.local:9090"
	DefaultPVCTemplate   = "data-{{POD}}"
	DefaultMaximumAge    = 2 * time.Minute
)

type StoragePreflightCode string

const (
	StoragePrimaryUnavailable StoragePreflightCode = "primary_unavailable"
	StorageNotPrimary         StoragePreflightCode = "not_primary"
	StoragePrimaryChanged     StoragePreflightCode = "primary_changed"
	StorageMetricsUnavailable StoragePreflightCode = "metrics_unavailable"
	StorageMetricMissing      StoragePreflightCode = "metric_missing"
	StorageMetricAmbiguous    StoragePreflightCode = "metric_ambiguous"
	StorageMetricStale        StoragePreflightCode = "metric_stale"
	StorageMetricInvalid      StoragePreflightCode = "metric_invalid"
	StorageMappingMissing     StoragePreflightCode = "mapping_missing"
	StorageMappingAmbiguous   StoragePreflightCode = "mapping_ambiguous"
	StorageInsufficientSpace  StoragePreflightCode = "insufficient_space"
)

type StoragePreflightError struct {
	Code        StoragePreflightCode
	Observation StorageObservation
	Cause       error
}

func (e *StoragePreflightError) Error() string {
	return fmt.Sprintf("storage preflight %s: %s", e.Code, safeFailure(e.Cause))
}
func (e *StoragePreflightError) Unwrap() error { return e.Cause }

type storageQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PrometheusStoragePreflight struct {
	DB            storageQueryRower
	HTTPClient    *http.Client
	PrometheusURL string
	PVCTemplate   string
	MaximumAge    time.Duration
	RetryDelays   []time.Duration
	Now           func() time.Time
}

var prometheusRetryDelays = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second}

type prometheusVectorResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (p PrometheusStoragePreflight) Check(ctx context.Context) (StorageObservation, error) {
	observation := StorageObservation{}
	if p.DB == nil || p.HTTPClient == nil || p.MaximumAge <= 0 || !strings.Contains(p.PVCTemplate, "{{POD}}") {
		return observation, preflightError(StorageMetricsUnavailable, observation, errors.New("invalid storage preflight configuration"))
	}
	base, err := url.Parse(p.PrometheusURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.Fragment != "" {
		return observation, preflightError(StorageMetricsUnavailable, observation, errors.New("invalid Prometheus URL"))
	}
	primary, err := p.primary(ctx)
	if err != nil {
		return observation, err
	}
	observation.PostgresPrimary = primary
	var primarySamples []prometheusSample
	var now time.Time
	delays := p.retryDelays()
	for attempt := 0; ; attempt++ {
		now = time.Now().UTC()
		if p.Now != nil {
			now = p.Now().UTC()
		}
		observation.CheckedAt = now
		replicationSamples, queryErr := p.queryWithTimestamps(ctx, base, "pg_replication_is_replica", "instance", now)
		if queryErr != nil {
			return observation, attachObservation(queryErr, observation)
		}
		primarySamples = primarySamples[:0]
		for _, sample := range replicationSamples {
			host, _, splitErr := net.SplitHostPort(sample.Metric["instance"])
			if splitErr == nil && host == primary {
				primarySamples = append(primarySamples, sample)
			}
		}
		if len(primarySamples) > 0 {
			break
		}
		if attempt >= len(delays) {
			return observation, preflightError(StorageMappingMissing, observation, errors.New("Prometheus has no PostgreSQL sample for the primary address"))
		}
		if err := waitForRetry(ctx, delays[attempt]); err != nil {
			return observation, preflightError(StorageMetricsUnavailable, observation, err)
		}
	}
	if len(primarySamples) != 1 {
		return observation, preflightError(StorageMappingAmbiguous, observation, errors.New("Prometheus has multiple PostgreSQL samples for the primary address"))
	}
	primarySample := primarySamples[0]
	if primarySample.Metric["__name__"] != "pg_replication_is_replica" || primarySample.Metric["pod"] == "" || primarySample.Metric["namespace"] == "" {
		return observation, preflightError(StorageMetricInvalid, observation, errors.New("primary mapping metric lacks pod or namespace labels"))
	}
	if err := p.validateSample(primarySample, now); err != nil {
		return observation, attachObservation(err, observation)
	}
	if primarySample.Value != 0 {
		return observation, preflightError(StorageNotPrimary, observation, errors.New("Prometheus identifies the SQL backend as a replica"))
	}
	observation.Namespace = primarySample.Metric["namespace"]
	observation.PodName = primarySample.Metric["pod"]
	observation.PVCName = strings.ReplaceAll(p.PVCTemplate, "{{POD}}", observation.PodName)
	available, err := p.storageMetric(ctx, base, "kubelet_volume_stats_available_bytes", observation, now)
	if err != nil {
		return observation, err
	}
	capacity, err := p.storageMetric(ctx, base, "kubelet_volume_stats_capacity_bytes", observation, now)
	if err != nil {
		return observation, err
	}
	observation.FreeBytes, observation.CapacityBytes = available.Value, capacity.Value
	observation.SampledAt = available.Timestamp
	observation.NodeName = available.NodeName
	if capacity.Timestamp.Before(observation.SampledAt) {
		observation.SampledAt = capacity.Timestamp
	}
	if observation.NodeName == "" {
		observation.NodeName = capacity.NodeName
	}
	if capacity.NodeName != "" && observation.NodeName != capacity.NodeName {
		return observation, preflightError(StorageMappingAmbiguous, observation, errors.New("storage metrics identify different Kubernetes nodes"))
	}
	if observation.CapacityBytes <= 0 || observation.FreeBytes > observation.CapacityBytes {
		return observation, preflightError(StorageMetricInvalid, observation, errors.New("impossible storage capacity or availability"))
	}
	observation.RequiredFreeBytes = observation.CapacityBytes / 10
	if observation.CapacityBytes%10 != 0 {
		observation.RequiredFreeBytes++
	}
	rechecked, err := p.primary(ctx)
	if err != nil || rechecked != primary {
		if err == nil {
			err = fmt.Errorf("primary changed from %s to %s", primary, rechecked)
		}
		return observation, preflightError(StoragePrimaryChanged, observation, err)
	}
	if observation.FreeBytes < observation.RequiredFreeBytes {
		return observation, preflightError(StorageInsufficientSpace, observation, fmt.Errorf("PVC %s has %d bytes free; %d required", observation.PVCName, observation.FreeBytes, observation.RequiredFreeBytes))
	}
	return observation, nil
}

type storageMetricObservation struct {
	Value     int64
	Timestamp time.Time
	NodeName  string
}

func (p PrometheusStoragePreflight) storageMetric(ctx context.Context, base *url.URL, metric string, observation StorageObservation, now time.Time) (storageMetricObservation, error) {
	query := fmt.Sprintf(`%s{namespace=%s,persistentvolumeclaim=%s}`, metric, strconv.Quote(observation.Namespace), strconv.Quote(observation.PVCName))
	samples, err := p.queryWithTimestamps(ctx, base, query, "persistentvolumeclaim", now)
	if err != nil {
		return storageMetricObservation{}, attachObservation(err, observation)
	}
	if len(samples) == 0 {
		return storageMetricObservation{}, preflightError(StorageMetricMissing, observation, fmt.Errorf("Prometheus returned no %s sample", metric))
	}
	if len(samples) != 1 {
		return storageMetricObservation{}, preflightError(StorageMetricAmbiguous, observation, fmt.Errorf("Prometheus returned multiple %s samples", metric))
	}
	sample := samples[0]
	if sample.Metric["__name__"] != metric || sample.Metric["namespace"] != observation.Namespace || sample.Metric["persistentvolumeclaim"] != observation.PVCName {
		return storageMetricObservation{}, preflightError(StorageMetricInvalid, observation, fmt.Errorf("Prometheus returned mismatched %s labels", metric))
	}
	if err := p.validateSample(sample, now); err != nil {
		return storageMetricObservation{}, attachObservation(err, observation)
	}
	return storageMetricObservation{Value: int64(sample.Value), Timestamp: sample.Timestamp, NodeName: sample.Metric["node"]}, nil
}

func (p PrometheusStoragePreflight) queryWithTimestamps(ctx context.Context, base *url.URL, query, identityLabel string, at time.Time) ([]prometheusSample, error) {
	values, err := p.query(ctx, base, query, at)
	if err != nil {
		return nil, err
	}
	timestamps, err := p.query(ctx, base, "timestamp("+query+")", at)
	if err != nil {
		return nil, err
	}
	byIdentity := make(map[string]time.Time, len(timestamps))
	for _, sample := range timestamps {
		if sample.Metric[identityLabel] == "" || sample.Value < 0 || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			return nil, preflightError(StorageMetricInvalid, StorageObservation{}, errors.New("invalid Prometheus source timestamp"))
		}
		identity := metricIdentity(sample.Metric)
		if _, duplicate := byIdentity[identity]; duplicate {
			return nil, preflightError(StorageMetricAmbiguous, StorageObservation{}, errors.New("duplicate Prometheus source timestamp"))
		}
		byIdentity[identity] = time.Unix(0, int64(sample.Value*float64(time.Second))).UTC()
	}
	if len(values) != len(byIdentity) {
		return nil, preflightError(StorageMetricMissing, StorageObservation{}, errors.New("Prometheus value and timestamp series differ"))
	}
	for index := range values {
		if values[index].Metric[identityLabel] == "" {
			return nil, preflightError(StorageMetricInvalid, StorageObservation{}, errors.New("Prometheus sample lacks its identity label"))
		}
		identity := metricIdentity(values[index].Metric)
		sourceTimestamp, ok := byIdentity[identity]
		if !ok {
			return nil, preflightError(StorageMetricMissing, StorageObservation{}, errors.New("Prometheus sample lacks a source timestamp"))
		}
		values[index].Timestamp = sourceTimestamp
	}
	return values, nil
}

func metricIdentity(metric map[string]string) string {
	labels := make([]string, 0, len(metric))
	for name, value := range metric {
		if name != "__name__" {
			labels = append(labels, name+"="+value)
		}
	}
	sort.Strings(labels)
	return strings.Join(labels, "\x00")
}

func (p PrometheusStoragePreflight) validateSample(sample prometheusSample, now time.Time) error {
	metric := sample.Metric["__name__"]
	if now.Sub(sample.Timestamp) > p.MaximumAge || sample.Timestamp.After(now) {
		return preflightError(StorageMetricStale, StorageObservation{}, fmt.Errorf("metric %s has stale timestamp", metric))
	}
	if sample.Value < 0 || sample.Value > math.MaxInt64 || math.Trunc(sample.Value) != sample.Value {
		return preflightError(StorageMetricInvalid, StorageObservation{}, fmt.Errorf("metric %s has invalid value", metric))
	}
	return nil
}

func (p PrometheusStoragePreflight) primary(ctx context.Context) (string, error) {
	var recovery bool
	var address *string
	if err := p.DB.QueryRow(ctx, `SELECT pg_is_in_recovery(),host(inet_server_addr())`).Scan(&recovery, &address); err != nil {
		return "", preflightError(StoragePrimaryUnavailable, StorageObservation{}, err)
	}
	if recovery {
		return "", preflightError(StorageNotPrimary, StorageObservation{}, errors.New("database connection is in recovery"))
	}
	if address == nil || strings.TrimSpace(*address) == "" {
		return "", preflightError(StoragePrimaryUnavailable, StorageObservation{}, errors.New("database backend address is unavailable"))
	}
	return *address, nil
}

type prometheusSample struct {
	Metric    map[string]string
	Timestamp time.Time
	Value     float64
}

func (p PrometheusStoragePreflight) query(ctx context.Context, base *url.URL, query string, at time.Time) ([]prometheusSample, error) {
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/query"
	parameters := endpoint.Query()
	parameters.Set("query", query)
	parameters.Set("time", at.Format(time.RFC3339Nano))
	endpoint.RawQuery = parameters.Encode()
	delays := p.retryDelays()
	var body []byte
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, preflightError(StorageMetricsUnavailable, StorageObservation{}, err)
		}
		response, requestErr := p.HTTPClient.Do(request)
		retryable := requestErr != nil && ctx.Err() == nil
		if requestErr == nil {
			body, requestErr = io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
			response.Body.Close()
			if requestErr != nil {
				retryable = ctx.Err() == nil
			}
			if requestErr == nil && len(body) > 1<<20 {
				return nil, preflightError(StorageMetricsUnavailable, StorageObservation{}, errors.New("Prometheus response exceeds one MiB"))
			}
			if requestErr == nil && response.StatusCode != http.StatusOK {
				requestErr = fmt.Errorf("Prometheus query failed with status %d", response.StatusCode)
				retryable = response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
			}
		}
		if requestErr == nil {
			break
		}
		if !retryable || attempt >= len(delays) {
			return nil, preflightError(StorageMetricsUnavailable, StorageObservation{}, requestErr)
		}
		if err := waitForRetry(ctx, delays[attempt]); err != nil {
			return nil, preflightError(StorageMetricsUnavailable, StorageObservation{}, err)
		}
	}
	var decoded prometheusVectorResponse
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Status != "success" || decoded.Data.ResultType != "vector" {
		return nil, preflightError(StorageMetricsUnavailable, StorageObservation{}, fmt.Errorf("invalid Prometheus response: %s", decoded.Error))
	}
	result := make([]prometheusSample, 0, len(decoded.Data.Result))
	for _, item := range decoded.Data.Result {
		if len(item.Value) != 2 {
			return nil, preflightError(StorageMetricInvalid, StorageObservation{}, errors.New("invalid Prometheus sample tuple"))
		}
		var timestamp float64
		var valueText string
		if err := json.Unmarshal(item.Value[0], &timestamp); err != nil || json.Unmarshal(item.Value[1], &valueText) != nil {
			return nil, preflightError(StorageMetricInvalid, StorageObservation{}, errors.New("invalid Prometheus sample encoding"))
		}
		value, err := strconv.ParseFloat(valueText, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, preflightError(StorageMetricInvalid, StorageObservation{}, errors.New("invalid Prometheus sample value"))
		}
		result = append(result, prometheusSample{Metric: item.Metric, Timestamp: time.Unix(0, int64(timestamp*float64(time.Second))).UTC(), Value: value})
	}
	return result, nil
}

func (p PrometheusStoragePreflight) retryDelays() []time.Duration {
	if p.RetryDelays != nil {
		return p.RetryDelays
	}
	return prometheusRetryDelays
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func preflightError(code StoragePreflightCode, observation StorageObservation, cause error) error {
	return &StoragePreflightError{Code: code, Observation: observation, Cause: cause}
}

func attachObservation(err error, observation StorageObservation) error {
	var blocked *StoragePreflightError
	if errors.As(err, &blocked) {
		blocked.Observation = observation
	}
	return err
}
