package osm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (p PrometheusStoragePreflight) checkConfigured(ctx context.Context) (StorageObservation, error) {
	observation := StorageObservation{}
	configuration := StorageMetricsConfig{PrometheusURL: p.PrometheusURL, AvailableMetric: p.AvailableMetric, CapacityMetric: p.CapacityMetric, SelectorLabels: p.SelectorLabels}
	if p.DB == nil || p.HTTPClient == nil || p.MaximumAge <= 0 || configuration.Validate() != nil {
		return observation, preflightError(StorageMetricsUnavailable, observation, errors.New("invalid configured storage metrics"))
	}
	primary := func() (string, error) {
		var recovery bool
		var address *string
		if err := p.DB.QueryRow(ctx, `SELECT pg_is_in_recovery(),host(inet_server_addr())`).Scan(&recovery, &address); err != nil {
			return "", err
		}
		if recovery {
			return "", errors.New("database connection is not writable")
		}
		if address == nil {
			return "", nil
		}
		return *address, nil
	}
	address, err := primary()
	if err != nil {
		return observation, preflightError(StoragePrimaryUnavailable, observation, err)
	}
	observation.PostgresPrimary = address
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	observation.CheckedAt = now
	base, _ := url.Parse(p.PrometheusURL)
	labels := make([]string, 0, len(p.SelectorLabels))
	for name, value := range p.SelectorLabels {
		labels = append(labels, name+"="+strconv.Quote(value))
	}
	sort.Strings(labels)
	read := func(metric string) (prometheusSample, error) {
		query := metric + "{" + strings.Join(labels, ",") + "}"
		values, err := p.queryWithTimestamps(ctx, base, query, "", now)
		if err != nil {
			return prometheusSample{}, err
		}
		if len(values) == 0 {
			return prometheusSample{}, preflightError(StorageMetricMissing, observation, errors.New("storage metric has no matching sample"))
		}
		if len(values) != 1 {
			return prometheusSample{}, preflightError(StorageMetricAmbiguous, observation, errors.New("storage metric must select exactly one filesystem"))
		}
		value := values[0]
		if value.Metric["__name__"] != metric {
			return value, preflightError(StorageMetricInvalid, observation, errors.New("unexpected storage metric"))
		}
		for name, expected := range p.SelectorLabels {
			if value.Metric[name] != expected {
				return value, preflightError(StorageMetricInvalid, observation, errors.New("storage metric selector mismatch"))
			}
		}
		return value, p.validateSample(value, now)
	}
	available, err := read(p.AvailableMetric)
	if err != nil {
		return observation, attachObservation(err, observation)
	}
	capacity, err := read(p.CapacityMetric)
	if err != nil {
		return observation, attachObservation(err, observation)
	}
	if metricIdentity(available.Metric) != metricIdentity(capacity.Metric) {
		return observation, preflightError(StorageMappingAmbiguous, observation, errors.New("storage metrics refer to different filesystems"))
	}
	observation.FreeBytes = int64(available.Value)
	observation.CapacityBytes = int64(capacity.Value)
	observation.SampledAt = available.Timestamp
	if capacity.Timestamp.Before(observation.SampledAt) {
		observation.SampledAt = capacity.Timestamp
	}
	observation.Namespace = available.Metric["namespace"]
	observation.PodName = available.Metric["pod"]
	observation.PVCName = available.Metric["persistentvolumeclaim"]
	observation.NodeName = available.Metric["node"]
	if observation.CapacityBytes <= 0 || observation.FreeBytes > observation.CapacityBytes {
		return observation, preflightError(StorageMetricInvalid, observation, errors.New("impossible storage capacity or availability"))
	}
	observation.RequiredFreeBytes = (observation.CapacityBytes / 10)
	if observation.CapacityBytes%10 != 0 {
		observation.RequiredFreeBytes++
	}
	rechecked, err := primary()
	if err != nil || rechecked != address {
		return observation, preflightError(StoragePrimaryChanged, observation, errors.New("database writer changed during storage check"))
	}
	if observation.FreeBytes < observation.RequiredFreeBytes {
		return observation, preflightError(StorageInsufficientSpace, observation, fmt.Errorf("database storage has %d bytes free; %d required", observation.FreeBytes, observation.RequiredFreeBytes))
	}
	return observation, nil
}
