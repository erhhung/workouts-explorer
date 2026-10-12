package telemetry

import (
	"context"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// identityExporter preserves producer identity when a gateway drops resource
// attributes. Only these four aliases are promoted, never arbitrary resources.
type identityExporter struct {
	metric.Exporter
}

func (e identityExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	var aliases []attribute.KeyValue
	for _, mapping := range [][2]string{
		{"service.name", "service_name"},
		{"service.namespace", "service_namespace"},
		{"deployment.environment.name", "deployment_environment"},
		{"service.instance.id", "service_instance_id"},
	} {
		value, ok := data.Resource.Set().Value(attribute.Key(mapping[0]))
		if ok && value.Type() == attribute.STRING && value.AsString() != "" {
			aliases = append(aliases, attribute.String(mapping[1], value.AsString()))
		}
	}
	if len(aliases) == 0 {
		return e.Exporter.Export(ctx, data)
	}

	// The SDK reuses export data. Copy every slice whose attributes we change;
	// measurement payloads and the immutable resource are left untouched.
	out := *data
	out.ScopeMetrics = slices.Clone(data.ScopeMetrics)
	for i := range out.ScopeMetrics {
		scope := &out.ScopeMetrics[i]
		scope.Metrics = slices.Clone(scope.Metrics)
		for j := range scope.Metrics {
			m := &scope.Metrics[j]
			switch aggregation := m.Data.(type) {
			case metricdata.Gauge[int64], metricdata.Sum[int64], metricdata.Histogram[int64], metricdata.ExponentialHistogram[int64]:
				m.Data = metricIdentity[int64](aggregation, aliases)
			case metricdata.Gauge[float64], metricdata.Sum[float64], metricdata.Histogram[float64], metricdata.ExponentialHistogram[float64]:
				m.Data = metricIdentity[float64](aggregation, aliases)
			case metricdata.Summary:
				aggregation.DataPoints = slices.Clone(aggregation.DataPoints)
				for k := range aggregation.DataPoints {
					point := &aggregation.DataPoints[k]
					point.Attributes = mergeIdentity(point.Attributes, aliases)
				}
				m.Data = aggregation
			}
		}
	}
	return e.Exporter.Export(ctx, &out)
}

func metricIdentity[N int64 | float64](data metricdata.Aggregation, aliases []attribute.KeyValue) metricdata.Aggregation {
	switch aggregation := data.(type) {
	case metricdata.Gauge[N]:
		aggregation.DataPoints = slices.Clone(aggregation.DataPoints)
		for i := range aggregation.DataPoints {
			point := &aggregation.DataPoints[i]
			point.Attributes = mergeIdentity(point.Attributes, aliases)
		}
		return aggregation
	case metricdata.Sum[N]:
		aggregation.DataPoints = slices.Clone(aggregation.DataPoints)
		for i := range aggregation.DataPoints {
			point := &aggregation.DataPoints[i]
			point.Attributes = mergeIdentity(point.Attributes, aliases)
		}
		return aggregation
	case metricdata.Histogram[N]:
		aggregation.DataPoints = slices.Clone(aggregation.DataPoints)
		for i := range aggregation.DataPoints {
			point := &aggregation.DataPoints[i]
			point.Attributes = mergeIdentity(point.Attributes, aliases)
		}
		return aggregation
	case metricdata.ExponentialHistogram[N]:
		aggregation.DataPoints = slices.Clone(aggregation.DataPoints)
		for i := range aggregation.DataPoints {
			point := &aggregation.DataPoints[i]
			point.Attributes = mergeIdentity(point.Attributes, aliases)
		}
		return aggregation
	default:
		return data
	}
}

func mergeIdentity(attrs attribute.Set, aliases []attribute.KeyValue) attribute.Set {
	// Resource identity wins over a conflicting alias on an individual metric.
	return attribute.NewSet(append(attrs.ToSlice(), aliases...)...)
}
