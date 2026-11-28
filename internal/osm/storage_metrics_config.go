package osm

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type StorageMetricsConfig struct {
	Enabled                                        bool
	PrometheusURL, AvailableMetric, CapacityMetric string
	SelectorLabels                                 map[string]string
}

var metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
var metricLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func StorageMetricsFromEnv(getenv func(string) string) (StorageMetricsConfig, error) {
	value := StorageMetricsConfig{PrometheusURL: strings.TrimSpace(getenv("OSM_PROMETHEUS_URL")),
		AvailableMetric: getenv("OSM_STORAGE_AVAILABLE_METRIC"), CapacityMetric: getenv("OSM_STORAGE_CAPACITY_METRIC")}
	flag := getenv("OSM_STORAGE_METRICS_ENABLED")
	if flag != "" {
		var err error
		value.Enabled, err = strconv.ParseBool(flag)
		if err != nil {
			return value, errors.New("invalid storage metrics enabled setting")
		}
	}
	value.Enabled = value.Enabled && value.PrometheusURL != ""
	if !value.Enabled {
		return value, nil
	}
	if err := json.Unmarshal([]byte(getenv("OSM_STORAGE_SELECTOR_LABELS_JSON")), &value.SelectorLabels); err != nil {
		return value, errors.New("invalid storage metrics selector labels")
	}
	return value, value.Validate()
}

func (c StorageMetricsConfig) Validate() error {
	endpoint, err := url.Parse(c.PrometheusURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.Fragment != "" ||
		!metricName.MatchString(c.AvailableMetric) || !metricName.MatchString(c.CapacityMetric) || c.AvailableMetric == c.CapacityMetric {
		return errors.New("invalid storage metrics endpoint or metric names")
	}
	for key, value := range c.SelectorLabels {
		if !metricLabelName.MatchString(key) || key == "__name__" || len(value) > 1024 {
			return errors.New("invalid storage metrics selector label")
		}
	}
	return nil
}
