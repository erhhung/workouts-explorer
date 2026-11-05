package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type tileRoundTripper func(*http.Request) (*http.Response, error)

func allowTileTestDelivery() bool { return true }

func (f tileRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type tileTestCall struct {
	request *http.Request
	reply   chan string
}

func tileTestService(t *testing.T, cfg config.MapTiles, ignoreCancellation bool) (*mapTileService, <-chan tileTestCall) {
	t.Helper()
	calls := make(chan tileTestCall, 32)
	client := &http.Client{Transport: tileRoundTripper(func(r *http.Request) (*http.Response, error) {
		call := tileTestCall{request: r, reply: make(chan string, 1)}
		calls <- call
		var body string
		if ignoreCancellation {
			body = <-call.reply
		} else {
			select {
			case body = <-call.reply:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-protobuf"}},
			Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := newMapTileService(context.Background(), cfg, "http://tiles.invalid", client)
	t.Cleanup(s.close)
	return s, calls
}

func tileTestKey() mapTileKey {
	return mapTileKey{account: uuid.New(), session: uuid.New(), selection: uuid.New(), generation: 7, z: 3, x: 1, y: 1}
}

func tileTestGet(s *mapTileService, ctx context.Context, key mapTileKey) <-chan mapTileResult {
	result := make(chan mapTileResult, 1)
	go func() {
		value := s.get(ctx, key, time.Now().Add(time.Minute))
		value.releaseBody()
		result <- value
	}()
	return result
}

func tileTestReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("tile test did not complete")
		var zero T
		return zero
	}
}

func tileTestState(t *testing.T, s *mapTileService, predicate func(*mapTileService) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		ok := predicate(s)
		s.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("tile service state did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTileCoalescingFirstWaiterCancellationAndCache(t *testing.T) {
	for _, coverage := range []bool{false, true} {
		t.Run(mapTileKey{coverage: coverage}.kind(), func(t *testing.T) {
			s, calls := tileTestService(t, config.MapTiles{}, false)
			key := tileTestKey()
			key.coverage = coverage
			ctx, cancel := context.WithCancel(context.Background())
			first := tileTestGet(s, ctx, key)
			call := tileTestReceive(t, calls)
			wantPath := "/app.raw_route_mvt/3/1/1"
			if coverage {
				wantPath = "/app.coverage_mvt/3/1/1"
			}
			if call.request.URL.Path != wantPath {
				t.Fatalf("upstream path=%q, want %q", call.request.URL.Path, wantPath)
			}
			query := call.request.URL.Query()
			if len(query) != 4 || query.Get("target_account_id") != key.account.String() ||
				query.Get("target_session_id") != key.session.String() || query.Get("target_selection_id") != key.selection.String() ||
				query.Get("target_generation") != "7" {
				t.Fatal("upstream did not receive exactly the authorized scope")
			}
			second := tileTestGet(s, context.Background(), key)
			tileTestState(t, s, func(s *mapTileService) bool { return s.jobs[key].waiters == 2 })
			cancel()
			if got := tileTestReceive(t, first); got.status != 503 {
				t.Fatalf("canceled first waiter=%+v", got)
			}
			if call.request.Context().Err() != nil {
				t.Fatal("first caller canceled shared request")
			}
			call.reply <- "private tile"
			if got := tileTestReceive(t, second); got.status != 200 || string(got.body) != "private tile" {
				t.Fatalf("follower=%+v", got)
			}
			hit := s.get(context.Background(), key, time.Now().Add(time.Minute))
			defer hit.releaseBody()
			if hit.status != 200 || string(hit.body) != "private tile" || !hit.forwardStarted.IsZero() {
				t.Fatalf("cache hit=%+v", hit)
			}
			select {
			case <-calls:
				t.Fatal("coalescing/cache issued an extra call")
			default:
			}
		})
	}
}

func TestTileAbandonedActiveRetainsSlotAndCannotOverwriteReplacement(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency = 1
	s, calls := tileTestService(t, cfg, true)
	key := tileTestKey()
	ctx, cancel := context.WithCancel(context.Background())
	first := tileTestGet(s, ctx, key)
	old := tileTestReceive(t, calls)
	cancel()
	tileTestReceive(t, first)
	if old.request.Context().Err() == nil {
		t.Fatal("last waiter did not cancel upstream")
	}
	replacement := tileTestGet(s, context.Background(), key)
	tileTestState(t, s, func(s *mapTileService) bool { return s.active == 1 && s.queue.Len() == 1 })
	select {
	case <-calls:
		t.Fatal("replacement exceeded active limit while old transport still running")
	default:
	}
	old.reply <- "abandoned secret"
	next := tileTestReceive(t, calls)
	tileTestState(t, s, func(s *mapTileService) bool { return len(s.cache) == 0 && s.jobs[key] != nil })
	next.reply <- "replacement"
	if got := tileTestReceive(t, replacement); string(got.body) != "replacement" || got.status != 200 {
		t.Fatalf("replacement=%+v", got)
	}
	hit := s.get(context.Background(), key, time.Now().Add(time.Minute))
	defer hit.releaseBody()
	if string(hit.body) != "replacement" {
		t.Fatal("old completion overwrote replacement cache")
	}
}

func TestTileFIFOQueueLimitsAndQueuedLastCancellation(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency, cfg.QueueSize = 1, 2
	s, calls := tileTestService(t, cfg, false)
	key := tileTestKey()
	first := tileTestGet(s, context.Background(), key)
	active := tileTestReceive(t, calls)
	a, b, c := key, key, key
	a.x, a.coverage, b.x, c.x = 2, true, 3, 4
	ctx, cancel := context.WithCancel(context.Background())
	queuedA := tileTestGet(s, ctx, a)
	tileTestState(t, s, func(s *mapTileService) bool { return s.queue.Len() == 1 })
	queuedB := tileTestGet(s, context.Background(), b)
	tileTestState(t, s, func(s *mapTileService) bool { return s.queue.Len() == 2 })
	if got := s.get(context.Background(), c, time.Now().Add(time.Minute)); got.outcome != "queue_full" {
		t.Fatalf("full queue=%+v", got)
	}
	cancel()
	tileTestReceive(t, queuedA)
	queuedC := tileTestGet(s, context.Background(), c)
	tileTestState(t, s, func(s *mapTileService) bool { return s.queue.Len() == 2 })
	active.reply <- "first"
	tileTestReceive(t, first)
	next := tileTestReceive(t, calls)
	if !strings.HasSuffix(next.request.URL.Path, "/3/3/1") {
		t.Fatalf("FIFO selected wrong job: %s", next.request.URL.Path)
	}
	next.reply <- "second"
	tileTestReceive(t, queuedB)
	next = tileTestReceive(t, calls)
	if !strings.HasSuffix(next.request.URL.Path, "/3/4/1") {
		t.Fatal("FIFO did not retain order after removal")
	}
	next.reply <- "third"
	tileTestReceive(t, queuedC)
}

func TestTileQueueTimeoutAndWholeJobDeadline(t *testing.T) {
	for _, queued := range []bool{true, false} {
		t.Run(map[bool]string{true: "queue", false: "active"}[queued], func(t *testing.T) {
			cfg := (config.MapTiles{}).WithDefaults()
			cfg.Concurrency = 1
			cfg.QueueTimeout = 20 * time.Millisecond
			if !queued {
				cfg.RequestTimeout = 20 * time.Millisecond
			}
			s, calls := tileTestService(t, cfg, true)
			key := tileTestKey()
			first := tileTestGet(s, context.Background(), key)
			call := tileTestReceive(t, calls)
			if queued {
				other := key
				other.x++
				result := tileTestReceive(t, tileTestGet(s, context.Background(), other))
				if result.status != 503 || result.outcome != "queue_timeout" {
					t.Fatalf("queue deadline=%+v", result)
				}
				call.reply <- "first"
				tileTestReceive(t, first)
			} else {
				result := tileTestReceive(t, first)
				if result.status != 503 || result.outcome != "deadline" {
					t.Fatalf("job deadline=%+v", result)
				}
				call.reply <- "late"
			}
			tileTestState(t, s, func(s *mapTileService) bool { return s.active == 0 && s.queue.Len() == 0 && len(s.jobs) == 0 })
		})
	}
}

func TestTileQueuedJobKeepsOriginalDeadlineAndFollower(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency = 1
	// A short internal budget makes the parent's fixed deadline observable
	// rather than the separate five-second forwarding cap.
	cfg.RequestTimeout = 2 * time.Second
	s, calls := tileTestService(t, cfg, false)
	key := tileTestKey()
	active := tileTestGet(s, context.Background(), key)
	first := tileTestReceive(t, calls)
	key.coverage = true
	ctx, cancel := context.WithCancel(context.Background())
	queued := tileTestGet(s, ctx, key)
	tileTestState(t, s, func(s *mapTileService) bool { return s.queue.Len() == 1 })
	follower := tileTestGet(s, context.Background(), key)
	tileTestState(t, s, func(s *mapTileService) bool { return s.jobs[key].waiters == 2 })
	s.mu.Lock()
	deadline, _ := s.jobs[key].ctx.Deadline()
	s.mu.Unlock()
	cancel()
	tileTestReceive(t, queued)
	tileTestState(t, s, func(s *mapTileService) bool { return s.queue.Len() == 1 && s.jobs[key].waiters == 1 })
	first.reply <- "route"
	tileTestReceive(t, active)
	next := tileTestReceive(t, calls)
	actual, _ := next.request.Context().Deadline()
	if !actual.Equal(deadline) {
		t.Fatal("queue dispatch reset the shared job's deadline")
	}
	next.reply <- "coverage"
	if result := tileTestReceive(t, follower); result.status != 200 || string(result.body) != "coverage" {
		t.Fatalf("queued follower result=%+v", result)
	}
}

func TestTileWaiterAndRequestBounds(t *testing.T) {
	s, calls := tileTestService(t, config.MapTiles{}, false)
	key := tileTestKey()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := []<-chan mapTileResult{tileTestGet(s, ctx, key)}
	tileTestReceive(t, calls)
	for range maximumTileWaiters - 1 {
		results = append(results, tileTestGet(s, ctx, key))
	}
	tileTestState(t, s, func(s *mapTileService) bool { return s.jobs[key].waiters == maximumTileWaiters })
	if got := s.get(ctx, key, time.Now().Add(time.Minute)); got.outcome != "waiter_limit" {
		t.Fatalf("waiter bound=%+v", got)
	}
	for range maximumTileRequests {
		if !s.admit() {
			t.Fatal("request capacity prematurely exhausted")
		}
	}
	if s.admit() {
		t.Fatal("unbounded admitted requests")
	}
	for range maximumTileRequests {
		<-s.requests
	}
	cancel()
	for _, result := range results {
		tileTestReceive(t, result)
	}
	tileTestState(t, s, func(s *mapTileService) bool { return s.active == 0 })
}

func TestTileCacheBoundsExpiryScopeAndEmpty(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.CacheBytes, cfg.CacheEntries = 6, 2
	var calls atomic.Int64
	client := &http.Client{Transport: tileRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body := "abc"
		if strings.Contains(r.URL.Path, "coverage") {
			body = ""
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-protobuf"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := newMapTileService(context.Background(), cfg, "http://tiles.invalid", client)
	defer s.close()
	key := tileTestKey()
	get := func(key mapTileKey) {
		t.Helper()
		got := s.get(context.Background(), key, time.Now().Add(time.Minute))
		defer got.releaseBody()
		if got.status != 200 {
			t.Fatalf("get=%+v", got)
		}
	}
	get(key)
	get(key)
	if calls.Load() != 1 {
		t.Fatal("cache miss on identical scope")
	}
	for _, mutate := range []func(*mapTileKey){
		func(k *mapTileKey) { k.account = uuid.New() }, func(k *mapTileKey) { k.session = uuid.New() },
		func(k *mapTileKey) { k.selection = uuid.New() }, func(k *mapTileKey) { k.generation++ },
		func(k *mapTileKey) { k.coverage = true }, func(k *mapTileKey) { k.z++ },
		func(k *mapTileKey) { k.x++ }, func(k *mapTileKey) { k.y++ },
	} {
		other := key
		mutate(&other)
		before := calls.Load()
		get(other)
		if calls.Load() != before+1 {
			t.Fatal("distinct scope reused cache")
		}
	}
	empty := key
	empty.coverage = true
	get(empty)
	before := calls.Load()
	get(empty)
	if calls.Load() != before {
		t.Fatal("empty successful tile was not cached")
	}
	s.mu.Lock()
	entry := s.cache[empty].Value.(*mapTileEntry)
	entry.expires = time.Now().Add(-time.Second)
	s.cache[empty].Value = entry
	s.mu.Unlock()
	get(empty)
	if calls.Load() != before+1 {
		t.Fatal("expired tile was served")
	}
	s.forget(empty)
	get(empty)
	s.mu.Lock()
	if s.cacheBytes > cfg.CacheBytes || s.lru.Len() > cfg.CacheEntries {
		t.Fatal("cache exceeds bounds")
	}
	s.mu.Unlock()

	// Completion cannot extend the capability lifetime, and hits do not slide TTL.
	s.forget(key)
	expires := time.Now().Add(200 * time.Millisecond)
	expiring := s.get(context.Background(), key, expires)
	expiring.releaseBody()
	s.mu.Lock()
	firstExpiry := s.cache[key].Value.(*mapTileEntry).expires
	s.mu.Unlock()
	get(key)
	s.mu.Lock()
	if firstExpiry.After(expires) || s.cache[key].Value.(*mapTileEntry).expires != firstExpiry {
		t.Fatal("cache extended authorization expiry or TTL")
	}
	s.mu.Unlock()
}

func TestTileZeroCacheAndZeroQueue(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency, cfg.QueueSize, cfg.CacheBytes = 1, 0, 0
	s, calls := tileTestService(t, cfg, false)
	key := tileTestKey()
	first := tileTestGet(s, context.Background(), key)
	call := tileTestReceive(t, calls)
	other := key
	other.x++
	if got := s.get(context.Background(), other, time.Now().Add(time.Minute)); got.outcome != "queue_full" {
		t.Fatal("explicit zero queue not respected")
	}
	call.reply <- "tile"
	tileTestReceive(t, first)
	second := tileTestGet(s, context.Background(), key)
	call = tileTestReceive(t, calls)
	call.reply <- "fresh"
	if got := tileTestReceive(t, second); string(got.body) != "fresh" {
		t.Fatal("disabled cache returned old body")
	}
}

func TestTileTransportAndResponsePrivacy(t *testing.T) {
	client := newMapTileClient(4)
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableCompression || transport.MaxConnsPerHost != 4 ||
		transport.MaxIdleConnsPerHost != 4 || transport.IdleConnTimeout == 0 || client.Timeout != 5*time.Second {
		t.Fatal("dedicated transport limits/privacy changed")
	}
	const canary = "CANARY-private-credential"
	for _, tc := range []struct {
		name, encoding, content, body string
		status                        int
	}{
		{"empty", "", "", "", 204},
		{"exact size", "identity", "application/x-protobuf", strings.Repeat("x", maximumTileBytes), 200},
		{"oversize", "", "application/x-protobuf", strings.Repeat("x", maximumTileBytes+1), 200},
		{"encoding", "gzip", "application/x-protobuf", canary, 200},
		{"html", "", "text/html", canary, 200},
		{"error", "", "application/x-protobuf", canary, 500},
		{"redirect", "", "application/x-protobuf", canary, 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "identity" || len(r.URL.Query()) != 4 {
					t.Error("credentials/unapproved parameters or encoding reached upstream")
				}
				w.Header().Set("Content-Type", tc.content)
				if tc.encoding != "" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				w.Header().Set("Set-Cookie", canary)
				w.Header().Set("Location", "/"+canary)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			s := newMapTileService(context.Background(), config.MapTiles{}, upstream.URL, nil)
			defer s.close()
			key := tileTestKey()
			for range 2 {
				result := s.get(context.Background(), key, time.Now().Add(time.Minute))
				request := httptest.NewRequest(http.MethodGet, "/?target_account_id="+canary, nil)
				request.Header.Set("Authorization", "Bearer "+canary)
				request.Header.Set("Cookie", canary)
				w := httptest.NewRecorder()
				writeMapTileResult(w, request, "route", result, allowTileTestDelivery)
				result.releaseBody()
				want := 502
				if tc.name == "empty" || tc.name == "exact size" {
					want = 200
				}
				if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Vary") != "Cookie, Authorization, Accept-Encoding" ||
					w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), canary) {
					t.Fatalf("unsafe response status=%d headers=%v", w.Code, w.Header())
				}
			}
			if tc.name != "empty" && tc.name != "exact size" && count.Load() != 2 {
				t.Fatal("failure was cached or redirect followed")
			}
		})
	}
}

func TestTileBodyTimeoutAndShutdownCancel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client := newMapTileClient(1)
	client.Timeout = 30 * time.Millisecond
	s := newMapTileService(context.Background(), config.MapTiles{}, upstream.URL, client)
	defer s.close()
	if got := s.get(context.Background(), tileTestKey(), time.Now().Add(time.Minute)); got.status != 502 || got.outcome != "body_error" {
		t.Fatalf("body timeout=%+v", got)
	}
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency = 1
	service, calls := tileTestService(t, cfg, false)
	key := tileTestKey()
	active := tileTestGet(service, context.Background(), key)
	call := tileTestReceive(t, calls)
	key.x++
	queued := tileTestGet(service, context.Background(), key)
	tileTestState(t, service, func(s *mapTileService) bool { return s.queue.Len() == 1 })
	service.close()
	if tileTestReceive(t, active).status != 503 || tileTestReceive(t, queued).status != 503 || call.request.Context().Err() == nil || service.admit() {
		t.Fatal("shutdown did not reject/cancel all work")
	}
	tileTestState(t, service, func(s *mapTileService) bool { return s.active == 0 && len(s.jobs) == 0 && s.queue.Len() == 0 })
}

func TestTileHeaderTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client := newMapTileClient(1)
	client.Timeout = 30 * time.Millisecond
	s := newMapTileService(context.Background(), config.MapTiles{}, upstream.URL, client)
	defer s.close()
	if got := s.get(context.Background(), tileTestKey(), time.Now().Add(time.Minute)); got.status != 502 || got.outcome != "upstream_error" {
		t.Fatalf("header timeout=%+v", got)
	}
}

func TestTileCanceledRequestNeverStarts(t *testing.T) {
	s, calls := tileTestService(t, config.MapTiles{}, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := s.get(ctx, tileTestKey(), time.Now().Add(time.Minute)); got.status != 503 {
		t.Fatal("canceled request accepted")
	}
	select {
	case <-calls:
		t.Fatal("canceled request dispatched")
	default:
	}
	writer := httptest.NewRecorder()
	writeMapTileResult(writer, httptest.NewRequest(http.MethodGet, "/", nil), "route", tileUnavailable("queue_full"), nil)
	if writer.Header().Get("Retry-After") != "1" {
		t.Fatal("admission retry header missing")
	}
	if !errors.Is(context.Canceled, ctx.Err()) {
		t.Fatal("unexpected test cancellation")
	}
}

type tileDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *tileDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestTileDeadlineBeforeAuthenticationAndPrivateValidationErrors(t *testing.T) {
	server := &Server{}
	defer server.mapTiles().close()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	defer cancel()
	original, _ := ctx.Deadline()
	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	w := &tileDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	server.GetMapSelectionRouteTile(w, request, generated.MapSelectionID(compactUUID(uuid.New())), 1, 0, 0, 0)
	if w.Code != 401 || w.deadline.IsZero() || w.deadline.After(original) ||
		w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Vary") != "Cookie, Authorization, Accept-Encoding" {
		t.Fatal("authentication response lacks original write deadline or private headers")
	}
	// A syntactically valid credential with an expired context must fail before
	// consulting a database (this server deliberately has none).
	token, _, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	request.Header.Set("Authorization", "Bearer "+token)
	w = &tileDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	server.GetMapSelectionCoverageTile(w, request, generated.MapSelectionID(compactUUID(uuid.New())), 1, 0, 0, 0)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("pre-authentication cancellation did not use bounded unavailable response")
	}
	handler := testHandler(t)
	for _, kind := range []string{"route", "coverage"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/api/map-selections/"+compactUUID(uuid.New())+"/"+kind+"-tiles/1/not-an-integer/0/0.pbf", nil))
		if w.Code != 400 || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Vary") != "Cookie, Authorization, Accept-Encoding" {
			t.Fatalf("contract validation error is cacheable: status=%d", w.Code)
		}
	}
}

func TestTileLifecycleContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var closed atomic.Bool
	client := &http.Client{Transport: &tileLifecycleTransport{closed: &closed}}
	s := newMapTileService(ctx, config.MapTiles{}, "http://tiles.invalid", client)
	cancel()
	tileTestState(t, s, func(s *mapTileService) bool { return s.closed && len(s.jobs) == 0 && len(s.cache) == 0 })
	deadline := time.Now().Add(time.Second)
	for !closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !closed.Load() || s.admit() {
		t.Fatal("lifecycle cancellation did not close idle connections and admission")
	}
}

type tileLifecycleTransport struct{ closed *atomic.Bool }

func (t *tileLifecycleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}

func (t *tileLifecycleTransport) CloseIdleConnections() { t.closed.Store(true) }

func TestTileCacheUsesLRUAndByteBound(t *testing.T) {
	for _, byteBound := range []bool{true, false} {
		cfg := (config.MapTiles{}).WithDefaults()
		cfg.CacheEntries = 2
		if byteBound {
			cfg.CacheBytes, cfg.CacheEntries = 6, 100
		}
		s := newMapTileService(context.Background(), cfg, "http://tiles.invalid", nil)
		key := tileTestKey()
		a, b, c := key, key, key
		b.x, c.x = 2, 3
		put := func(k mapTileKey, body string) {
			buffer := make([]byte, len(body))
			copy(buffer, body)
			s.cacheLocked(&mapTileJob{key: k, expires: time.Now().Add(time.Minute)}, mapTileResult{body: buffer, status: 200})
		}
		s.mu.Lock()
		put(a, "aaa")
		put(b, "bbb")
		s.lru.MoveToFront(s.cache[a])
		put(c, "ccc")
		if s.cache[b] != nil || s.cache[a] == nil || s.cache[c] == nil || s.cacheBytes != 6 {
			t.Fatal("LRU/byte eviction retained the wrong entries")
		}
		if byteBound {
			put(b, "1234567")
			if s.cache[b] != nil || s.cacheBytes != 6 {
				t.Fatal("oversized cache object disturbed retained entries")
			}
		}
		s.mu.Unlock()
		s.close()
	}
}

func TestTileMetricsContainOnlyBoundedDimensions(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	const canary = "CANARY-do-not-log-URL-or-credentials"
	s := newMapTileService(context.Background(), config.MapTiles{}, "http://tiles.invalid", &http.Client{
		Transport: tileRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New(canary) }),
	})
	defer s.close()
	key := tileTestKey()
	result := s.get(context.Background(), key, time.Now().Add(time.Minute))
	w := httptest.NewRecorder()
	writeMapTileResult(w, httptest.NewRequest(http.MethodGet, "/", nil), key.kind(), result, nil)
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, scope := range collected.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			seen[instrument.Name] = true
			sets := []metricdata.DataPoint[int64]{}
			switch data := instrument.Data.(type) {
			case metricdata.Sum[int64]:
				sets = data.DataPoints
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					sets = append(sets, metricdata.DataPoint[int64]{Attributes: point.Attributes})
				}
			}
			for _, point := range sets {
				for _, attribute := range point.Attributes.ToSlice() {
					if attribute.Key != "tile_kind" && attribute.Key != "outcome" && attribute.Key != "http.response.status_code" {
						t.Fatalf("unbounded metric dimension %s", attribute.Key)
					}
					value := attribute.Value.Emit()
					if strings.Contains(value, canary) || strings.Contains(value, key.account.String()) || strings.Contains(value, key.selection.String()) || strings.Contains(value, "http://") {
						t.Fatal("private metric dimension")
					}
				}
			}
		}
	}
	for _, name := range []string{"workouts.api.tile.events", "workouts.api.tile.active", "workouts.api.tile.queue.duration", "workouts.api.tile.duration"} {
		if !seen[name] {
			t.Fatalf("missing tile instrument %s", name)
		}
	}
}
