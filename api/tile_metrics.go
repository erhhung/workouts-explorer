package api

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type mapTileMetrics struct {
	events    metric.Int64Counter
	active    metric.Int64UpDownCounter
	occupied  metric.Int64UpDownCounter
	queued    metric.Int64UpDownCounter
	queueWait metric.Float64Histogram
}

func newMapTileMetrics() mapTileMetrics {
	meter := otel.Meter("github.com/erhhung/workouts-explorer/api")
	var m mapTileMetrics
	var err error
	m.events, err = meter.Int64Counter("workouts.api.tile.events", metric.WithDescription("Bounded private tile cache and request outcomes"))
	if err != nil {
		otel.Handle(err)
	}
	m.active, err = meter.Int64UpDownCounter("workouts.api.tile.active", metric.WithDescription("Active private tile upstream calls"))
	if err != nil {
		otel.Handle(err)
	}
	m.occupied, err = meter.Int64UpDownCounter("workouts.api.tile.occupied", metric.WithDescription("Private tile render and delivery body slots"))
	if err != nil {
		otel.Handle(err)
	}
	m.queued, err = meter.Int64UpDownCounter("workouts.api.tile.queued", metric.WithDescription("Queued distinct private tile jobs"))
	if err != nil {
		otel.Handle(err)
	}
	m.queueWait, err = meter.Float64Histogram("workouts.api.tile.queue.duration", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(.01, .05, .1, .25, .5, 1, 2, 3))
	if err != nil {
		otel.Handle(err)
	}
	return m
}

func (m mapTileMetrics) event(ctx context.Context, key mapTileKey, outcome string) {
	m.events.Add(ctx, 1, metric.WithAttributes(attribute.String("tile_kind", key.kind()), attribute.String("outcome", outcome)))
}

func recordTileForwarding(ctx context.Context, kind string, result mapTileResult) {
	if result.forwardStarted.IsZero() {
		return
	}
	duration, err := otel.Meter("github.com/erhhung/workouts-explorer/api").Float64Histogram(
		"workouts.api.tile.duration", metric.WithDescription("Private tile forwarding through response body write"),
		metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.01, .05, .1, .25, .5, 1, 2.5, 5, 10))
	if err != nil {
		otel.Handle(err)
		return
	}
	duration.Record(ctx, time.Since(result.forwardStarted).Seconds(), metric.WithAttributes(
		attribute.String("tile_kind", kind), attribute.String("outcome", result.outcome),
		attribute.Int("http.response.status_code", result.status)))
}

func privateTileHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	vary := strings.Join(w.Header().Values("Vary"), ", ")
	for _, token := range []string{"Cookie", "Authorization", "Accept-Encoding"} {
		found := false
		for _, existing := range strings.Split(vary, ",") {
			if strings.EqualFold(strings.TrimSpace(existing), token) || strings.TrimSpace(existing) == "*" {
				found = true
				break
			}
		}
		if !found {
			if vary != "" {
				vary += ", "
			}
			vary += token
		}
	}
	w.Header().Set("Vary", vary)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeMapTileResult(w http.ResponseWriter, r *http.Request, kind string, result mapTileResult, authorize func() bool) {
	defer func() { recordTileForwarding(r.Context(), kind, result) }()
	privateTileHeaders(w)
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")
	var encoder *gzip.Writer
	if result.status == http.StatusOK && len(result.body) >= minimumTileGzipBytes && acceptsTileGzip(r.Header) {
		if r.Context().Err() == nil {
			select {
			case encoder = <-tileCompressors:
				defer func() {
					if encoder != nil {
						encoder.Reset(io.Discard)
					}
					tileCompressors <- encoder
				}()
				if r.Context().Err() == nil {
					if encoder == nil {
						encoder, _ = gzip.NewWriterLevel(w, gzip.BestSpeed)
					} else {
						encoder.Reset(w)
					}
				}
			case <-r.Context().Done():
			}
		}
		if r.Context().Err() != nil {
			result.status, result.outcome = http.StatusServiceUnavailable, "canceled"
		}
	}
	if result.status == http.StatusOK {
		if authorize == nil {
			result.status, result.outcome = http.StatusServiceUnavailable, "authorization_error"
		} else if !authorize() {
			// Authorization owns its problem response, not a forwarding outcome.
			result.forwardStarted = time.Time{}
			return
		}
	}
	if result.status != http.StatusOK {
		if result.status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "1")
			writeProblem(w, r, result.status, "Service Unavailable", "private map tiles are temporarily unavailable")
		} else {
			writeProblem(w, r, http.StatusBadGateway, "Bad Gateway", "private map tiles are temporarily unavailable")
		}
		return
	}
	w.Header().Set("Content-Type", "application/vnd.mapbox-vector-tile")
	if encoder != nil {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.WriteHeader(http.StatusOK)
	if encoder != nil {
		_, writeErr := encoder.Write(result.body)
		closeErr := encoder.Close()
		if writeErr != nil || closeErr != nil {
			result.outcome = "write_error"
		}
	} else if _, err := w.Write(result.body); err != nil {
		result.outcome = "write_error"
	}
}
