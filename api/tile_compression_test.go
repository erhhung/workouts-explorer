package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestTileGzipNegotiation(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []string
		gzip   bool
	}{
		{"absent", nil, false},
		{"empty", []string{""}, false},
		{"browser", []string{"gzip, deflate, br, zstd"}, true},
		{"case and whitespace", []string{" GZip ; Q = 0.500 "}, true},
		{"refused", []string{"gzip;q=0"}, false},
		{"wildcard", []string{"br, *;q=0.5"}, true},
		{"wildcard refused", []string{"*;q=0"}, false},
		{"explicit beats wildcard", []string{"gzip;q=0, *;q=1"}, false},
		{"explicit allowed", []string{"*;q=0, gzip;q=0.001"}, true},
		{"identity preferred", []string{"gzip;q=0.5, identity;q=1"}, false},
		{"gzip preferred", []string{"gzip;q=0.8, identity;q=0.2"}, true},
		{"identity refused", []string{"identity;q=0, gzip"}, true},
		{"both refused", []string{"identity;q=0, gzip;q=0"}, false},
		{"equal preference", []string{"identity;q=1, gzip;q=1.000"}, true},
		{"multiple headers", []string{"br", "gzip;q=0.9"}, true},
		{"duplicate weighted", []string{"gzip;q=1", "gzip;q=0.5, identity;q=0.8"}, false},
		{"duplicate refusal", []string{"gzip;q=0", "gzip;q=1"}, false},
		{"reverse duplicate refusal", []string{"gzip;q=1, gzip;q=0"}, false},
		{"not a token match", []string{"xgzip, gzip2"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{"Accept-Encoding": test.values}
			if got := acceptsTileGzip(header); got != test.gzip {
				t.Fatalf("gzip=%t, want %t", got, test.gzip)
			}
		})
	}
	for _, quality := range []string{"", "-1", "+1", "2", "1.001", "0.0001", ".5", "NaN", "Inf", "1e0", "\"1\"", "0.5;q=1", "1;other=1"} {
		header := http.Header{"Accept-Encoding": {"gzip;q=" + quality + ", *;q=1"}}
		if acceptsTileGzip(header) {
			t.Errorf("malformed quality %q enabled gzip", quality)
		}
	}
}

// Synthetic MVT v2: separate identified point features, no private route data.
func syntheticCompressionTile() []byte {
	layer := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "routes")
	layer = protowire.AppendVarint(protowire.AppendTag(layer, 15, protowire.VarintType), 2)
	layer = protowire.AppendVarint(protowire.AppendTag(layer, 5, protowire.VarintType), 4096)
	for id := uint64(1); id <= 30000; id++ {
		feature := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), id)
		feature = protowire.AppendVarint(protowire.AppendTag(feature, 3, protowire.VarintType), 1)
		feature = protowire.AppendBytes(protowire.AppendTag(feature, 4, protowire.BytesType), []byte{9, 2, 2})
		layer = protowire.AppendBytes(protowire.AppendTag(layer, 2, protowire.BytesType), feature)
	}
	return protowire.AppendBytes(protowire.AppendTag(nil, 3, protowire.BytesType), layer)
}

func TestTileGzipSuccessRoundTripAndHeaders(t *testing.T) {
	tile := syntheticCompressionTile()
	for _, test := range []struct {
		name, accept string
		body         []byte
		status       int
		gzip         bool
	}{
		{"gzip", "gzip", tile, 200, true},
		{"identity", "identity", tile, 200, false},
		{"absent", "", tile, 200, false},
		{"refused", "gzip;q=0, *;q=1", tile, 200, false},
		{"small", "gzip", tile[:minimumTileGzipBytes-1], 200, false},
		{"threshold", "gzip", tile[:minimumTileGzipBytes], 200, true},
		{"empty", "gzip", nil, 200, false},
		{"error", "gzip", tile, 502, false},
		{"unavailable", "gzip", tile, 503, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept-Encoding", test.accept)
			w := httptest.NewRecorder()
			w.Header().Set("Content-Length", "999999")
			w.Header().Set("Content-Encoding", "stale")
			writeMapTileResult(w, r, "route", mapTileResult{status: test.status, body: test.body, outcome: "success"}, allowTileTestDelivery)
			if w.Code != test.status || (w.Header().Get("Content-Encoding") == "gzip") != test.gzip ||
				w.Header().Get("Content-Length") != "" || w.Header().Get("Cache-Control") != "private, no-store" ||
				w.Header().Get("Vary") != "Cookie, Authorization, Accept-Encoding" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("status=%d headers=%v", w.Code, w.Header())
			}
			body := w.Body.Bytes()
			if test.gzip {
				reader, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				body, err = io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
				if test.name == "gzip" {
					t.Logf("synthetic MVT: raw=%d gzip=%d ratio=%.4f", len(tile), w.Body.Len(), float64(w.Body.Len())/float64(len(tile)))
				}
			} else if w.Header().Get("Content-Encoding") != "" {
				t.Fatal("identity response retained content encoding")
			}
			if test.status == 200 {
				if !bytes.Equal(body, test.body) || w.Header().Get("Content-Type") != "application/vnd.mapbox-vector-tile" || w.Header().Get("Retry-After") != "" {
					t.Fatal("successful tile body or headers changed")
				}
			} else if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") || bytes.Equal(body, test.body) {
				t.Fatal("error exposed private tile bytes or incorrect media type")
			}
		})
	}
	w := httptest.NewRecorder()
	w.Header().Add("Vary", "Origin, cookie")
	w.Header().Add("Vary", "Authorization")
	privateTileHeaders(w)
	privateTileHeaders(w)
	if w.Header().Get("Vary") != "Origin, cookie, Authorization, Accept-Encoding" {
		t.Fatalf("Vary overwritten or duplicated: %v", w.Header().Values("Vary"))
	}
}

func TestTileGzipCacheRetainsRawLimitAndImmutableBytes(t *testing.T) {
	s, calls := tileTestService(t, config.MapTiles{}, false)
	key := tileTestKey()
	pending := tileTestLease(s, context.Background(), key)
	call := tileTestReceive(t, calls)
	if call.request.Header.Get("Accept-Encoding") != "identity" {
		t.Fatal("internal upstream compression changed")
	}
	call.reply <- strings.Repeat("x", maximumTileBytes)
	first := tileTestReceive(t, pending)
	first.releaseBody()
	for _, accept := range []string{"gzip", "identity", "gzip"} {
		result := s.get(context.Background(), key, time.Now().Add(time.Minute))
		if result.status != 200 || len(result.body) != maximumTileBytes || &result.body[0] != &first.body[0] {
			t.Fatal("cache did not retain one immutable raw body")
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", accept)
		w := httptest.NewRecorder()
		writeMapTileResult(w, r, key.kind(), result, allowTileTestDelivery)
		result.releaseBody()
		body := w.Body.Bytes()
		if accept == "gzip" {
			reader, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			_ = reader.Close()
		}
		if !bytes.Equal(body, first.body) || !bytes.Equal(first.body, bytes.Repeat([]byte("x"), maximumTileBytes)) {
			t.Fatal("delivery changed cached bytes or decompressed raw size")
		}
	}
	select {
	case <-calls:
		t.Fatal("encoding negotiation changed the raw cache key")
	default:
	}
}

func TestTileGzipHTTPClientAutomaticallyDecodes(t *testing.T) {
	tile := syntheticCompressionTile()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeMapTileResult(w, r, "route", mapTileResult{status: 200, body: tile, outcome: "success"}, allowTileTestDelivery)
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || !response.Uncompressed || !bytes.Equal(body, tile) {
		t.Fatal("automatic HTTP decompression did not preserve raw tile bytes")
	}
}

type tileCompressionWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	resume  chan struct{}
	writes  int
	failAt  int
	blockAt int
}

func (w *tileCompressionWriter) Write(body []byte) (int, error) {
	w.writes++
	if w.entered != nil && w.writes == max(w.blockAt, 1) {
		close(w.entered)
		<-w.resume
	}
	if w.writes == w.failAt {
		return 0, errors.New("private writer error must not be logged")
	}
	return w.ResponseRecorder.Write(body)
}

func TestTileGzipConcurrencyBoundAndWaitingCancellation(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	result := mapTileResult{body: bytes.Repeat([]byte("x"), minimumTileGzipBytes), status: 200, outcome: "success"}
	resume := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	done := make(chan struct{}, maximumTileCompressors)
	for range maximumTileCompressors {
		w := &tileCompressionWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), resume: resume}
		go func() {
			writeMapTileResult(w, r, "route", result, allowTileTestDelivery)
			done <- struct{}{}
		}()
		tileTestReceive(t, w.entered)
	}
	if len(tileCompressors) != 0 {
		t.Fatal("blocked delivery did not retain its encoder")
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	waiting := httptest.NewRecorder()
	started, finished := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		writeMapTileResult(waiting, r.WithContext(ctx), "coverage", result, func() bool {
			t.Error("canceled encoder waiter attempted delivery authorization")
			return false
		})
		close(finished)
	}()
	tileTestReceive(t, started)
	select {
	case <-finished:
		t.Fatal("delivery exceeded the encoder bound")
	default:
	}
	cancel()
	tileTestReceive(t, finished)
	if waiting.Code != 503 || waiting.Header().Get("Content-Encoding") != "" || waiting.Header().Get("Retry-After") != "1" ||
		waiting.Header().Get("Vary") != "Cookie, Authorization, Accept-Encoding" || bytes.Contains(waiting.Body.Bytes(), result.body) {
		t.Fatalf("unsafe canceled compression wait: status=%d headers=%v", waiting.Code, waiting.Header())
	}
	release()
	for range maximumTileCompressors {
		tileTestReceive(t, done)
	}
	if len(tileCompressors) != maximumTileCompressors {
		t.Fatal("completed deliveries did not release every encoder")
	}
}

func TestTileGzipDurationIncludesClose(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := &tileCompressionWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), resume: make(chan struct{}), blockAt: 2}
	release := sync.OnceFunc(func() { close(w.resume) })
	t.Cleanup(release)
	done := make(chan struct{})
	go func() {
		writeMapTileResult(w, r, "coverage", mapTileResult{body: bytes.Repeat([]byte("x"), minimumTileGzipBytes), status: 200,
			outcome: "success", forwardStarted: time.Now()}, allowTileTestDelivery)
		close(done)
	}()
	tileTestReceive(t, w.entered)
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name == "workouts.api.tile.duration" {
				t.Fatal("forwarding duration was recorded before gzip.Close completed")
			}
		}
	}
	release()
	tileTestReceive(t, done)
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name == "workouts.api.tile.duration" {
				points := instrument.Data.(metricdata.Histogram[float64]).DataPoints
				if len(points) == 1 && points[0].Count == 1 && points[0].Sum > 0 {
					return
				}
			}
		}
	}
	t.Fatal("completed gzip delivery did not record forwarding duration")
}

func TestTileGzipWriteAndCloseErrorsReleaseEncoderAndRecordMetrics(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		previous := otel.GetMeterProvider()
		otel.SetMeterProvider(provider)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := &tileCompressionWriter{ResponseRecorder: httptest.NewRecorder(), failAt: failAt}
		writeMapTileResult(w, r, "route", mapTileResult{body: bytes.Repeat([]byte("x"), minimumTileGzipBytes), status: 200,
			outcome: "success", forwardStarted: time.Now()}, allowTileTestDelivery)
		var collected metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &collected)
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, scope := range collected.ScopeMetrics {
			for _, instrument := range scope.Metrics {
				if instrument.Name == "workouts.api.tile.duration" {
					point := instrument.Data.(metricdata.Histogram[float64]).DataPoints[0]
					for _, attribute := range point.Attributes.ToSlice() {
						if attribute.Key == "outcome" {
							found = attribute.Value.AsString() == "write_error"
						}
					}
				}
			}
		}
		if !found || w.Code != 200 || w.writes != failAt || len(tileCompressors) != maximumTileCompressors {
			t.Fatalf("write/close error not recorded or encoder leaked: failAt=%d writes=%d", failAt, w.writes)
		}
	}
}

func TestTileDeliveryAuthorizationOwnsDeniedResponse(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	result := mapTileResult{status: 200, body: bytes.Repeat([]byte("PRIVATE-TILE-BYTES"), 100), outcome: "success", forwardStarted: time.Now()}
	for _, accept := range []string{"identity", "gzip"} {
		for _, status := range []int{401, 403, 404, 503} {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept-Encoding", accept)
			w := httptest.NewRecorder()
			called := false
			writeMapTileResult(w, r, "route", result, func() bool {
				called = true
				available := maximumTileCompressors
				if accept == "gzip" {
					available--
				}
				if len(tileCompressors) != available || w.Body.Len() != 0 || w.Header().Get("Content-Encoding") != "" {
					t.Error("authorization did not follow encoder admission and precede private output")
				}
				writeProblem(w, r, status, http.StatusText(status), "delivery authorization denied")
				return false
			})
			if !called || w.Code != status || w.Body.String() == "" || strings.Contains(w.Body.String(), "PRIVATE-TILE-BYTES") ||
				w.Header().Get("Content-Encoding") != "" || w.Header().Get("Content-Type") != "application/problem+json" ||
				len(tileCompressors) != maximumTileCompressors {
				t.Fatalf("denied delivery changed response or leaked encoder: status=%d headers=%v", w.Code, w.Header())
			}
		}
	}
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name == "workouts.api.tile.duration" {
				t.Fatal("authorization failure was recorded as a forwarding outcome")
			}
		}
	}
	for _, status := range []int{502, 503} {
		failed := result
		failed.status = status
		writeMapTileResult(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "route", failed, func() bool {
			t.Error("failed upstream result attempted delivery authorization")
			return false
		})
	}
	w := httptest.NewRecorder()
	writeMapTileResult(w, httptest.NewRequest(http.MethodGet, "/", nil), "route", result, nil)
	if w.Code != 503 || strings.Contains(w.Body.String(), "PRIVATE-TILE-BYTES") {
		t.Fatal("missing delivery authorization did not fail closed")
	}
}
