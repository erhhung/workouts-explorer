package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestViewportPlan(t *testing.T) {
	tiles, center, err := viewportPlan(3095, 1973, .0195, .0195, 14)
	if err != nil || math.Abs(center[0]-8192.887466666667) > 1e-10 || math.Abs(center[1]-8191.112533316201) > 1e-10 {
		t.Fatalf("settled Mercator center=%v err=%v", center, err)
	}
	// Independent settled MapLibre order; in particular the feature-bearing
	// central tiles must not be queued after the empty viewport margins.
	coords := [][2]int{{8192, 8191}, {8192, 8190}, {8193, 8191}, {8193, 8190}, {8191, 8191}, {8192, 8192}, {8191, 8190}, {8193, 8192}, {8192, 8189}, {8194, 8191}, {8193, 8189}, {8194, 8190}, {8191, 8192}, {8191, 8189}, {8194, 8192}, {8194, 8189}, {8190, 8191}, {8192, 8193}, {8190, 8190}, {8193, 8193}, {8195, 8191}, {8195, 8190}, {8190, 8192}, {8191, 8193}, {8190, 8189}, {8194, 8193}, {8195, 8192}, {8195, 8189}, {8190, 8193}, {8189, 8191}, {8189, 8190}, {8195, 8193}, {8189, 8192}, {8189, 8189}, {8189, 8193}}
	want := make([]viewportTile, len(coords))
	for i, c := range coords {
		want[i] = viewportTile{14, c[0], c[1]}
	}
	if !reflect.DeepEqual(tiles, want) {
		t.Fatalf("35 ordered tile plan differs: %v", tiles)
	}
	wide, _, err := viewportPlan(3280, 1973, .0195, .0195, 14)
	if err != nil || len(wide) != 40 {
		t.Fatalf("3280 CSS width must not be mistaken for final 3095: %d %v", len(wide), err)
	}
	// Exact half-open edges exclude touching neighbors; fractional zoom shrinks
	// the span rather than rounding the viewport or multiplying by DPR.
	for _, zoom := range []float64{14, 14.5} {
		edges, _, err := viewportPlan(1024*math.Exp2(zoom-14), 1024*math.Exp2(zoom-14), 0, 0, zoom)
		if err != nil || len(edges) != 4 {
			t.Fatalf("half-open/fractional plan=%v err=%v", edges, err)
		}
	}
	for _, v := range [][5]float64{{0, 1973, 0, 0, 14}, {3095, 1973, 0, 86, 14}, {3095, 1973, 0, 0, 23}, {math.NaN(), 1973, 0, 0, 14}, {3095, 1973, 180, 0, 14}} {
		if _, _, err := viewportPlan(v[0], v[1], v[2], v[3], v[4]); err == nil {
			t.Fatalf("invalid plan accepted: %v", v)
		}
	}
}

func viewportTestSelection(now time.Time) generated.MapSelection {
	return generated.MapSelection{Id: "0123456789ABCDEF0123456789ABCDEF", DataGeneration: 7, ExpiresAt: now.Add(30 * time.Minute),
		RouteTileUrl:    "/api/map-selections/0123456789ABCDEF0123456789ABCDEF/route-tiles/7/{z}/{x}/{y}.pbf",
		CoverageTileUrl: "/api/map-selections/0123456789ABCDEF0123456789ABCDEF/coverage-tiles/7/{z}/{x}/{y}.pbf"}
}

func TestViewportSourceURLsAndExpiry(t *testing.T) {
	now := time.Now()
	tiles, _, _ := viewportPlan(3095, 1973, .0195, .0195, 14)
	s := viewportTestSelection(now)
	for _, source := range []string{"route", "coverage"} {
		for _, limit := range []int{1, 6, 16, 35} {
			urls, err := viewportURLs(s, source, tiles, now, limit)
			if err != nil || len(urls) != 35 || !strings.Contains(urls[0], "/"+source+"-tiles/7/14/8192/8191.pbf") {
				t.Fatalf("actual source template: %v %v", urls, err)
			}
		}
	}
	for _, change := range []func(*generated.MapSelection){
		func(s *generated.MapSelection) { s.RouteTileUrl = "" },
		func(s *generated.MapSelection) { s.RouteTileUrl = "http://foreign.invalid/private/{z}/{x}/{y}" },
		func(s *generated.MapSelection) { s.RouteTileUrl = strings.ReplaceAll(s.RouteTileUrl, "/7/", "/8/") },
		func(s *generated.MapSelection) { s.ExpiresAt = now.Add(35 * time.Second) },
		func(s *generated.MapSelection) { s.DataGeneration = 0 },
	} {
		bad := s
		change(&bad)
		if _, err := viewportURLs(bad, "route", tiles, now, 35); err == nil {
			t.Fatal("invalid response/lifetime accepted")
		}
	}
	s.ExpiresAt = now.Add(36 * time.Second)
	if _, err := viewportURLs(s, "route", tiles, now, 35); err != nil {
		t.Fatal("exact minimum concurrent expiry rejected")
	}
	if _, err := viewportURLs(s, "route", tiles, now, 1); err == nil {
		t.Fatal("serial timeout reserve not enforced")
	}
	if _, err := viewportURLs(s, "route", tiles, now, 0); err == nil {
		t.Fatal("zero limit accepted")
	}
	if _, err := viewportURLs(s, "route", append(tiles, tiles[0]), now, 35); err == nil {
		t.Fatal("duplicate dense-tile URL accepted")
	}
	for _, base := range []string{viewportBase + "/", "http://localhost:18081", "https://127.0.0.1:18081", viewportBase + "?x=1", "http://user@127.0.0.1:18081"} {
		if viewportAPIBase(base) == nil {
			t.Fatal("unsafe API base accepted")
		}
	}
	if viewportAPIBase(viewportBase) != nil {
		t.Fatal("loopback rejected")
	}
	for _, value := range []string{"0", "11", "five", "5.5", "-1"} {
		if _, err := viewportBatchCount(value); err == nil {
			t.Fatal("unbounded batch count accepted")
		}
	}
	for value, want := range map[string]int{"": 5, "1": 1, "10": 10} {
		if got, err := viewportBatchCount(value); err != nil || got != want {
			t.Fatal("batch default/bounds")
		}
	}
}

func viewportTestBytes(message []byte, number protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(message, number, protowire.BytesType), value)
}

func TestViewportPilotWarnings(t *testing.T) {
	input := acceptanceProfileInput{Manifest: acceptanceTestManifest(), Name: "typical", SHA256: "unit-profile-hash"}
	for _, batches := range []int{1, 5, 10} {
		artifact, _, err := viewportArtifact(input, batches)
		if err != nil {
			t.Fatal(err)
		}
		warnings := artifact["warnings"].([]string)
		if len(warnings) != 1 || !strings.Contains(warnings[0], fmt.Sprintf("%d batches", batches)) || !strings.Contains(warnings[0], "not a stable SLO") {
			t.Fatalf("pilot warning must describe actual batch count: %v", warnings)
		}
	}
}

func viewportTestMVT(layerName string, props map[string]any, withFeature bool) []byte {
	layer := viewportTestBytes(nil, 1, []byte(layerName))
	layer = protowire.AppendVarint(protowire.AppendTag(layer, 15, protowire.VarintType), 2)
	layer = protowire.AppendVarint(protowire.AppendTag(layer, 5, protowire.VarintType), 4096)
	if withFeature {
		var tags []byte
		i := uint64(0)
		for k, v := range props {
			layer = viewportTestBytes(layer, 3, []byte(k))
			var value []byte
			if s, ok := v.(string); ok {
				value = viewportTestBytes(nil, 1, []byte(s))
			} else {
				value = protowire.AppendVarint(protowire.AppendTag(nil, 5, protowire.VarintType), uint64(v.(int64)))
			}
			layer = viewportTestBytes(layer, 4, value)
			tags = protowire.AppendVarint(protowire.AppendVarint(tags, i), i)
			i++
		}
		f := viewportTestBytes(nil, 2, tags)
		f = protowire.AppendVarint(protowire.AppendTag(f, 3, protowire.VarintType), 2)
		f = viewportTestBytes(f, 4, []byte{9, 0, 0, 10, 2, 2})
		layer = viewportTestBytes(layer, 2, f)
	}
	return viewportTestBytes(nil, 3, layer)
}

func TestViewportMVTContent(t *testing.T) {
	tile := viewportTile{14, 8192, 8191}
	routeID, pathID := "11111111111111111111111111111111", "22222222222222222222222222222222"
	props := map[string]any{"entityKind": "path", "name": "Grid Road 1", "localityName": "Synthetic Acceptance City", "broadClass": "road", "regionId": "fixture:dense", "rangeWorkoutCount": int64(40), "countBucket": int64(6), "entityId": pathID}
	w := generated.MapSelectionWorkout{Id: routeID, Type: generated.MapSelectionWorkoutType{Key: "outdoor-run", Name: "Outdoor Run"}}
	o := viewportOracle{Routes: map[string]generated.MapSelectionWorkout{routeID: w}, Paths: map[string]map[string]any{pathID: props}, Tiles: map[string]map[viewportTile]map[string]bool{"route": {tile: {routeID: true}}, "coverage": {tile: {pathID: true}}}}
	route := viewportTestMVT("routes", map[string]any{"workoutId": routeID, "workoutTypeKey": w.Type.Key, "workoutType": w.Type.Name, "sortOrder": int64(0)}, true)
	coverage := viewportTestMVT("coverage", props, true)
	for source, body := range map[string][]byte{"route": route, "coverage": coverage} {
		if n, err := o.validate(source, tile, body); err != nil || n != 1 {
			t.Fatalf("%s valid MVT n=%d err=%v", source, n, err)
		}
	}
	if _, err := o.validate("route", tile, nil); err == nil {
		t.Fatal("fake empty tile accepted against positive SQL oracle")
	}
	for _, body := range [][]byte{[]byte("not MVT"), {0x1a, 0xff}, {0x08, 0x01}, {0x1a, 0x01, 0x00}, route[:len(route)-1], viewportTestMVT("wrong_layer", props, true), append(coverage, coverage...)} {
		if _, err := o.validate("coverage", tile, body); err == nil {
			t.Fatal("malformed/wrong/duplicate MVT accepted")
		}
	}
	bad := map[string]any{}
	for k, v := range props {
		bad[k] = v
	}
	bad["countBucket"] = int64(5)
	if _, err := o.validate("coverage", tile, viewportTestMVT("coverage", bad, true)); err == nil {
		t.Fatal("SQL bucket mismatch accepted")
	}
	bad["countBucket"] = int64(6)
	bad["rangeWorkoutCount"] = "40"
	if _, err := o.validate("coverage", tile, viewportTestMVT("coverage", bad, true)); err == nil {
		t.Fatal("string count accepted instead of SQL integer")
	}
	bad["rangeWorkoutCount"] = int64(40)
	bad["entityId"] = routeID
	if _, err := o.validate("coverage", tile, viewportTestMVT("coverage", bad, true)); err == nil {
		t.Fatal("foreign identity accepted")
	}
	emptyTile := viewportTile{14, 8189, 8189}
	o.Tiles["route"][emptyTile] = map[string]bool{}
	for _, body := range [][]byte{nil, viewportTestMVT("routes", nil, false)} {
		if n, err := o.validate("route", emptyTile, body); err != nil || n != 0 {
			t.Fatalf("legitimate empty tile n=%d err=%v", n, err)
		}
	}
	if _, err := o.validate("route", emptyTile, viewportTestMVT("coverage", nil, false)); err == nil {
		t.Fatal("wrong empty source layer accepted")
	}
	// Packed tag indexes and odd tag lists are checked before indexing slices.
	for _, tags := range [][]byte{{0}, {0, 0}, {0xff}} {
		f := viewportTestBytes(nil, 2, tags)
		layer := viewportTestBytes(nil, 1, []byte("routes"))
		layer = viewportTestBytes(layer, 2, f)
		layer = protowire.AppendVarint(protowire.AppendTag(layer, 15, protowire.VarintType), 2)
		layer = protowire.AppendVarint(protowire.AppendTag(layer, 5, protowire.VarintType), 4096)
		if _, err := viewportDecodeMVT(viewportTestBytes(nil, 3, layer), "route"); err == nil {
			t.Fatal("invalid tag indexes accepted")
		}
	}
}

func TestViewportMVTValueTypes(t *testing.T) {
	varint := func(number protowire.Number, value uint64) []byte {
		return protowire.AppendVarint(protowire.AppendTag(nil, number, protowire.VarintType), value)
	}
	fixed32 := func(value float32) []byte {
		return protowire.AppendFixed32(protowire.AppendTag(nil, 2, protowire.Fixed32Type), math.Float32bits(value))
	}
	fixed64 := func(value float64) []byte {
		return protowire.AppendFixed64(protowire.AppendTag(nil, 3, protowire.Fixed64Type), math.Float64bits(value))
	}
	payload := func(value []byte) []byte {
		layer := viewportTestBytes(nil, 1, []byte("routes"))
		layer = protowire.AppendVarint(protowire.AppendTag(layer, 15, protowire.VarintType), 2)
		layer = protowire.AppendVarint(protowire.AppendTag(layer, 5, protowire.VarintType), 4096)
		layer = viewportTestBytes(layer, 3, []byte("attribute"))
		layer = viewportTestBytes(layer, 4, value)
		feature := viewportTestBytes(nil, 2, []byte{0, 0})
		feature = protowire.AppendVarint(protowire.AppendTag(feature, 3, protowire.VarintType), 2)
		feature = viewportTestBytes(feature, 4, []byte{9, 0, 0, 10, 2, 2})
		return viewportTestBytes(nil, 3, viewportTestBytes(layer, 2, feature))
	}
	negative := int64(-7)
	for _, test := range []struct {
		name  string
		value []byte
		want  any
	}{
		{"string", viewportTestBytes(nil, 1, []byte("text")), "text"},
		{"float", fixed32(1.25), float64(1.25)},
		{"double", fixed64(1.125), float64(1.125)},
		{"int", varint(4, uint64(negative)), negative},
		{"uint", varint(5, 40), int64(40)},
		{"large_uint", varint(5, math.MaxUint64), uint64(math.MaxUint64)},
		{"sint", varint(6, protowire.EncodeZigZag(negative)), negative},
		{"true", varint(7, 1), true},
		{"false", varint(7, 0), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			features, err := viewportDecodeMVT(payload(test.value), "route")
			if err != nil || len(features) != 1 || !reflect.DeepEqual(features[0].Properties["attribute"], test.want) {
				t.Fatalf("MVT value decode: %v %v", features, err)
			}
		})
	}
	for _, value := range [][]byte{varint(7, 2), fixed32(float32(math.Inf(1))), fixed64(math.NaN()), fixed64(math.Inf(-1)), fixed32(1)[:2], fixed64(1)[:3], varint(2, 1), append(varint(4, 1), varint(5, 2)...)} {
		if _, err := viewportDecodeMVT(payload(value), "route"); err == nil {
			t.Fatal("invalid/nonfinite/truncated/ambiguous MVT value accepted")
		}
	}
}

func TestViewportSchedulerPeakAndBodyCompletion(t *testing.T) {
	for _, limit := range []int{1, 6, 16, 35} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entered := make(chan struct{}, 35)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var mu sync.Mutex
			seen := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen[r.URL.Path]++
				mu.Unlock()
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				entered <- struct{}{}
				select {
				case <-release:
					_, _ = w.Write([]byte{0})
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			tiles := make([]viewportTile, 35)
			urls := make([]string, 35)
			for i := range tiles {
				tiles[i] = viewportTile{14, 8190 + i, 8191}
				urls[i] = fmt.Sprintf("%s/tile/%d", server.URL, i)
			}
			client := viewportClient(limit)
			defer client.CloseIdleConnections()
			transport := client.Transport.(*http.Transport)
			if transport.MaxConnsPerHost != limit || transport.ForceAttemptHTTP2 || transport.Proxy != nil {
				t.Fatal("transport does not enforce per-host HTTP1 cap")
			}
			finished := make(chan viewportBatch, 1)
			go func() {
				finished <- viewportBurst(ctx, client, "fake-local-token", "route", tiles, urls, limit, func(_ viewportTile, data []byte) (int, error) {
					if len(data) != 1 {
						return 0, errors.New("body incomplete")
					}
					return 0, nil
				})
			}()
			for range limit {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("first wave never reached requested concurrency")
				}
			}
			select {
			case b := <-finished:
				t.Fatalf("burst completed before body release: %+v", b)
			default:
			}
			mu.Lock()
			count := len(seen)
			mu.Unlock()
			if count != limit {
				t.Fatalf("body-blocked first wave=%d want %d", count, limit)
			}
			once.Do(func() { close(release) })
			var b viewportBatch
			select {
			case b = <-finished:
			case <-ctx.Done():
				t.Fatal("burst did not complete")
			}
			if b.Status != "PASS" || len(b.Samples) != 35 || b.PeakActive != limit || b.PeakConnected != limit {
				t.Fatalf("peak/results=%+v", b)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 35 {
				t.Fatalf("distinct URLs=%d", len(seen))
			}
			for _, n := range seen {
				if n != 1 {
					t.Fatal("tile retried")
				}
			}
			for _, s := range b.Samples {
				if s.Status != 200 || s.Protocol != "HTTP/1.1" || s.BodyComplete.Before(s.DispatchedAt) || s.ScheduledMS < s.RequestMS || s.Bytes != 1 {
					t.Fatal("invalid body-complete timing")
				}
			}
		})
	}
}

func TestViewportErrorsNoRetryOrRedirect(t *testing.T) {
	var leaked, attempts atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		switch r.URL.Path {
		case "/404":
			w.WriteHeader(404)
		case "/502":
			w.WriteHeader(502)
		case "/redirect":
			http.Redirect(w, r, foreign.URL, 302)
		}
	}))
	defer server.Close()
	tiles := []viewportTile{{14, 1, 1}, {14, 2, 1}, {14, 3, 1}}
	client := viewportClient(3)
	defer client.CloseIdleConnections()
	b := viewportBurst(context.Background(), client, "never-forward-this", "coverage", tiles, []string{server.URL + "/404", server.URL + "/502", server.URL + "/redirect"}, 3, func(viewportTile, []byte) (int, error) { t.Error("error response was decoded"); return 0, nil })
	if attempts.Load() != 3 || leaked.Load() != 0 || b.Status != "FAIL" || b.FailureClass != "UNSTABLE" || len(b.Samples) != 3 {
		t.Fatalf("retry/redirect/error retention: %+v attempts=%d leaked=%d", b, attempts.Load(), leaked.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := viewportBurst(ctx, client, "", "route", tiles, []string{server.URL, server.URL, server.URL}, 3, func(viewportTile, []byte) (int, error) { return 0, nil })
	for _, s := range cancelled.Samples {
		if s.ErrorClass != "cancelled" {
			t.Fatalf("cancellation lost: %+v", s)
		}
	}
	if viewportErrorClass(context.DeadlineExceeded) != "timeout" {
		t.Fatal("deadline classification")
	}
	c := viewportCase{Batches: []viewportBatch{b, cancelled}}
	viewportAggregate(&c)
	if c.Attempts != 6 || c.Errors != 6 || c.Successes != 0 || c.Status != "FAIL" || c.Statuses[502] != 1 || c.FailureTotals["cancelled"] != 3 {
		t.Fatalf("errors not aggregated: %+v", c)
	}
	encoded, _ := json.Marshal(c)
	for _, secret := range []string{server.URL, foreign.URL, "never-forward-this", "Authorization"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("unsafe artifact content")
		}
	}
}

func TestViewportBodyTimeoutRetained(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := viewportClient(1)
	client.Timeout = 100 * time.Millisecond
	defer client.CloseIdleConnections()
	tiles := []viewportTile{{14, 1, 1}, {14, 2, 1}, {14, 3, 1}}
	b := viewportBurst(context.Background(), client, "", "route", tiles, []string{server.URL, server.URL, server.URL}, 1, func(viewportTile, []byte) (int, error) { return 0, errors.New("must not decode timeout") })
	c := viewportCase{Batches: []viewportBatch{b}}
	viewportAggregate(&c)
	if attempts.Load() != 3 || c.Attempts != 3 || c.Timeouts != 3 || c.Errors != 3 || c.Statuses[200] != 3 || c.Completion.MaxMS < 250 {
		t.Fatalf("body timeouts/status/critical path not retained: %+v", c)
	}
}

func TestViewportAggregateCriticalPath(t *testing.T) {
	c := viewportCase{Batches: []viewportBatch{{Status: "PASS", CompletionMS: 2500, PeakActive: 6, Samples: []viewportSample{{RequestMS: 100, ScheduledMS: 100, Status: 200, Protocol: "HTTP/1.1", Features: 2, Bytes: 40}, {RequestMS: 200, ScheduledMS: 2500, Status: 200, Protocol: "HTTP/1.1"}}}, {Status: "PASS", CompletionMS: 2800, Samples: []viewportSample{{RequestMS: 300, ScheduledMS: 2800, Status: 200, Protocol: "HTTP/1.1"}}}}}
	viewportAggregate(&c)
	if c.Overall.P95MS != 300 || c.Completion.P95MS != 2800 || c.FeatureBearing.Samples != 1 || c.Empty.Samples != 2 || c.FeatureCounts != 2 || c.Status != "PASS" {
		t.Fatalf("critical path/group aggregation: %+v", c)
	}
	c.Batches[1].CompletionMS = 3001
	c = viewportCase{Batches: c.Batches}
	viewportAggregate(&c)
	if c.Status != "FAIL" || c.RequestBudget != "PASS" || c.ViewportBudget != "FAIL" {
		t.Fatal("distinct fetch/request budgets not enforced")
	}
	if got := viewportPercentiles([]float64{5, 1, 4, 2, 3}); got.P95MS != 5 || got.P50MS != 3 {
		t.Fatal("pilot nearest-rank percentiles")
	}
}

func TestViewportSerializedNodeContract(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is required for the Go/Node serialization contract")
	}
	raw, err := os.ReadFile("../deploy/acceptance/profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("COVERAGE_ACCEPTANCE_NAMESPACE_UID", "22222222-2222-4222-8222-222222222222")
	for _, name := range []string{"typical", "stress"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("COVERAGE_ACCEPTANCE_PROFILE", name)
			input, err := acceptanceDecodeProfile(raw, name)
			if err != nil {
				t.Fatal(err)
			}
			batches, err := viewportBatchCount("")
			if err != nil {
				t.Fatal(err)
			}
			artifact, tiles, err := viewportArtifact(input, batches)
			if err != nil {
				t.Fatal(err)
			}
			stamp := artifact["started_at"].(time.Time)
			created := stamp.Add(-time.Minute).Format(time.RFC3339Nano)
			artifact["namespace_created_at"] = created
			artifact["content_checks"] = map[string]any{"status": "PASS", "grid_size": 40, "named_paths_sql_oracle": 80, "routed_workouts_sql_oracle": input.routedCount(), "per_tile_SQL_cardinality": true}
			artifact["state_start"], artifact["state_end"] = viewportState{Generation: 1}, viewportState{Generation: 1}
			// Controlled-clock unit records, not performance evidence. Only the
			// production constructors, aggregate, finalizer, and JSON types define
			// the payload. It goes over stdin, never to an acceptance artifact path.
			cases, probes := []viewportCase{}, []viewportBatch{}
			clock := stamp.Add(time.Second)
			for _, source := range []string{"route", "coverage"} {
				for _, limit := range []int{1, 6, 16, 35} {
					c := viewportNewCase(input, source, limit)
					c.StartedAt = clock
					for batch := 1; batch <= batches; batch++ {
						b := viewportBatch{Index: batch, Status: "PASS", StartedAt: clock, PeakActive: limit, PeakConnected: limit, Generation: 1, GenerationStart: 1, GenerationEnd: 1, ExpiresAt: clock.Add(30 * time.Minute)}
						for i, tile := range tiles {
							queueMS := float64(i / limit)
							s := viewportSample{Source: source, Tile: tile, ScheduledAt: clock, DispatchedAt: clock.Add(time.Duration(queueMS) * time.Millisecond), QueueMS: queueMS, RequestMS: 1, ScheduledMS: queueMS + 1, Status: 200, Protocol: "HTTP/1.1"}
							s.BodyComplete = s.DispatchedAt.Add(time.Millisecond)
							if i == 0 {
								s.Features, s.Bytes = 1, 10
							}
							b.Samples = append(b.Samples, s)
							b.CompletionMS = max(b.CompletionMS, s.ScheduledMS)
						}
						b.FinishedAt = clock.Add(time.Duration(b.CompletionMS) * time.Millisecond)
						c.Batches = append(c.Batches, b)
						clock = b.FinishedAt.Add(time.Millisecond)
					}
					viewportAggregate(&c)
					c.RecordedAt = clock
					if c.Status != "PASS" {
						t.Fatalf("controlled case failed: %+v", c)
					}
					if limit == 1 {
						probes = append(probes, c.Batches[0])
					}
					cases = append(cases, c)
				}
			}
			artifact["status"] = "PASS"
			viewportFinishArtifact(artifact, cases, probes, map[string]int{}, clock)
			encoded, err := json.MarshalIndent(artifact, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["account_count"] != float64(1) || decoded["attempts"] != float64(8*batches*len(tiles)) {
				t.Fatal("serialized root scope/count contract")
			}
			metadataJSON, _ := json.Marshal(input.metadata())
			var metadata map[string]any
			if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
				t.Fatal(err)
			}
			for _, item := range decoded["cases"].([]any) {
				c := item.(map[string]any)
				p := c["provenance"].(map[string]any)
				for key, value := range metadata {
					if c[key] != value || p[key] != value {
						t.Fatalf("serialized case/provenance lacks inherited metadata %s", key)
					}
				}
				if p["kind"] != "captured_fenced_run" {
					t.Fatal("case provenance kind missing")
				}
			}
			variants := []map[string]any{{"name": "positive", "value": decoded}}
			for _, mutation := range []struct {
				name   string
				change func(map[string]any)
			}{
				{"root_schema", func(v map[string]any) { v["schema_version"] = 2 }},
				{"case_schema", func(v map[string]any) { v["cases"].([]any)[0].(map[string]any)["schema_version"] = 2 }},
				{"missing_case_schema", func(v map[string]any) { delete(v["cases"].([]any)[0].(map[string]any), "schema_version") }},
				{"case_profile_version", func(v map[string]any) { v["cases"].([]any)[0].(map[string]any)["profile_version"] = 99 }},
				{"case_profile_hash", func(v map[string]any) { v["cases"].([]any)[0].(map[string]any)["profile_manifest_sha256"] = "wrong" }},
				{"case_namespace", func(v map[string]any) { v["cases"].([]any)[0].(map[string]any)["namespace_uid"] = "old-namespace" }},
				{"case_provenance", func(v map[string]any) {
					v["cases"].([]any)[0].(map[string]any)["provenance"].(map[string]any)["namespace_uid"] = "old-namespace"
				}},
				{"account_count", func(v map[string]any) { delete(v, "account_count") }},
				{"missing_tile", func(v map[string]any) { p := v["tile_plan"].(map[string]any); p["tiles"] = p["tiles"].([]any)[1:] }},
				{"invalid_peak", func(v map[string]any) {
					v["cases"].([]any)[0].(map[string]any)["batches"].([]any)[0].(map[string]any)["peak_offered_active_requests"] = 2
				}},
				{"invalid_sample_timing", func(v map[string]any) {
					v["cases"].([]any)[0].(map[string]any)["batches"].([]any)[0].(map[string]any)["samples"].([]any)[0].(map[string]any)["request_ms"] = -1
				}},
			} {
				var v map[string]any
				if err := json.Unmarshal(encoded, &v); err != nil {
					t.Fatal(err)
				}
				mutation.change(v)
				variants = append(variants, map[string]any{"name": mutation.name, "value": v})
			}
			// Produce a functional failure through the real Go aggregate/finalizer,
			// while retaining every sample, case, and successful timing budget.
			failed := viewportNewCase(input, "route", 1)
			failed.StartedAt = cases[0].StartedAt
			failed.Batches = append([]viewportBatch(nil), cases[0].Batches...)
			failed.Batches[0].Samples = append([]viewportSample(nil), failed.Batches[0].Samples...)
			failed.Batches[0].Status = "FAIL"
			failed.Batches[0].Samples[0].Status = 502
			failed.Batches[0].Samples[0].ErrorClass = "http_status"
			failed.Batches[0].Samples[0].Features = 0
			viewportAggregate(&failed)
			failed.RecordedAt = cases[0].RecordedAt
			failedCases := append([]viewportCase(nil), cases...)
			failedCases[0] = failed
			var failedArtifact map[string]any
			if err := json.Unmarshal(encoded, &failedArtifact); err != nil {
				t.Fatal(err)
			}
			failedArtifact["status"] = "FAIL"
			viewportFinishArtifact(failedArtifact, failedCases, probes, map[string]int{failed.Name: 1}, clock)
			variants = append(variants, map[string]any{"name": "http_failure", "value": failedArtifact})
			payload, err := json.Marshal(map[string]any{"namespace": map[string]any{"metadata": map[string]any{"uid": os.Getenv("COVERAGE_ACCEPTANCE_NAMESPACE_UID"), "creationTimestamp": created}}, "variants": variants})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "-e", `
const fs=require("node:fs"),e=require("../deploy/acceptance/evidence.cjs");
const {namespace,variants}=JSON.parse(fs.readFileSync(0,"utf8"));
const results=variants.map(({name,value})=>{
  const before=JSON.stringify(value);
  let binding_rejected=false;
  try { e.assertMeasurementBinding(value,namespace); } catch { binding_rejected=true; }
  const assessment=e.assessViewport(value);
  return {name,binding_rejected,mutated:JSON.stringify(value)!==before,assessment};
});
process.stdout.write(JSON.stringify(results));`)
			cmd.Stdin = bytes.NewReader(payload)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("actual Node evidence validator: %v: %s", err, output)
			}
			var results []struct {
				Name            string `json:"name"`
				BindingRejected bool   `json:"binding_rejected"`
				Mutated         bool   `json:"mutated"`
				Assessment      struct {
					Status  string   `json:"status"`
					Request string   `json:"request_p95_target"`
					Fetch   string   `json:"viewport_fetch_target"`
					Render  string   `json:"initial_map_target"`
					Missing []string `json:"missing_evidence"`
					Cases   map[string]struct {
						Status string `json:"status"`
					} `json:"cases"`
				} `json:"assessment"`
			}
			if err := json.Unmarshal(output, &results); err != nil {
				t.Fatalf("decode actual Node assessment: %v: %s", err, output)
			}
			if len(results) != len(variants) {
				t.Fatal("Node omitted serialized contract variants")
			}
			for _, result := range results {
				if result.Mutated {
					t.Fatal("Node assessment mutated the Go artifact")
				}
				switch result.Name {
				case "positive":
					if result.BindingRejected || result.Assessment.Status != "PASS" || len(result.Assessment.Missing) != 0 || len(result.Assessment.Cases) != len(cases) || result.Assessment.Request != "PASS" || result.Assessment.Fetch != "PASS" || result.Assessment.Render != "NOT_MEASURED" {
						t.Fatalf("actual Go JSON rejected by Node: %s", output)
					}
					for name, c := range result.Assessment.Cases {
						if c.Status != "PASS" {
							t.Fatalf("Node rejected serialized case %s", name)
						}
					}
				case "http_failure":
					if result.BindingRejected || result.Assessment.Status != "FAIL" || len(result.Assessment.Missing) != 0 || result.Assessment.Request != "PASS" || result.Assessment.Fetch != "PASS" {
						t.Fatalf("Node lost Go functional failure: %s", output)
					}
				case "case_provenance":
					if !result.BindingRejected || result.Assessment.Status != "INCOMPLETE" || len(result.Assessment.Missing) == 0 {
						t.Fatal("Node accepted wrong nested provenance namespace")
					}
				default:
					if result.Assessment.Status != "INCOMPLETE" || len(result.Assessment.Missing) == 0 {
						t.Fatalf("Node accepted invalid serialized variant %s: %s", result.Name, output)
					}
					if strings.Contains(result.Name, "schema") || strings.Contains(result.Name, "profile") || result.Name == "case_namespace" {
						if !result.BindingRejected {
							t.Fatalf("Node accepted unbound %s", result.Name)
						}
					}
				}
			}
			t.Logf("actual Go JSON -> Node %s: positive PASS (%d bound cases, %d samples); %d negative variants rejected; browser render NOT_MEASURED", name, len(cases), 8*batches*len(tiles), len(variants)-1)
		})
	}
}
