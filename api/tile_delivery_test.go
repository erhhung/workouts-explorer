package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
)

func tileTestLease(s *mapTileService, ctx context.Context, key mapTileKey) <-chan mapTileResult {
	result := make(chan mapTileResult, 1)
	go func() { result <- s.get(ctx, key, time.Now().Add(time.Minute)) }()
	return result
}

func TestTileDeliveryLeasesBoundDistinctBodies(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency, cfg.CacheBytes = 2, 0
	s, calls := tileTestService(t, cfg, false)
	key := tileTestKey()
	first := tileTestLease(s, context.Background(), key)
	call := tileTestReceive(t, calls)
	follower := tileTestLease(s, context.Background(), key)
	tileTestState(t, s, func(s *mapTileService) bool { return s.jobs[key].waiters == 2 })
	call.reply <- strings.Repeat("a", maximumTileBytes)
	a, shared := tileTestReceive(t, first), tileTestReceive(t, follower)
	defer a.releaseBody()
	defer shared.releaseBody()
	if len(a.body) != maximumTileBytes || &a.body[0] != &shared.body[0] {
		t.Fatal("coalesced deliveries must share one immutable body")
	}
	other := key
	other.x++
	second := tileTestLease(s, context.Background(), other)
	call = tileTestReceive(t, calls)
	call.reply <- strings.Repeat("b", maximumTileBytes)
	b := tileTestReceive(t, second)
	defer b.releaseBody()
	tileTestState(t, s, func(s *mapTileService) bool { return s.active == 0 && s.occupied == 2 })

	other.x++
	queued := tileTestLease(s, context.Background(), other)
	tileTestState(t, s, func(s *mapTileService) bool { return s.queue.Len() == 1 })
	// Ending one follower's write cannot release the shared body's slot.
	a.releaseBody()
	a.releaseBody()
	s.mu.Lock()
	retained := s.occupied == 2 && s.queue.Len() == 1
	s.mu.Unlock()
	if !retained {
		t.Fatal("delivery released a slot before its last follower finished")
	}
	select {
	case <-calls:
		t.Fatal("slow deliveries allowed another distinct body allocation")
	default:
	}
	shared.releaseBody()
	call = tileTestReceive(t, calls)
	call.reply <- "next"
	c := tileTestReceive(t, queued)
	c.releaseBody()
	b.releaseBody()
	tileTestState(t, s, func(s *mapTileService) bool { return s.occupied == 0 && s.active == 0 })
}

func TestTileCacheDeliveryPinsStayInsideByteBudget(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency, cfg.CacheBytes, cfg.CacheEntries = 1, maximumTileBytes+1, 1
	var calls int
	client := &http.Client{Transport: tileRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-protobuf"}},
			Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maximumTileBytes)))}, nil
	})}
	s := newMapTileService(context.Background(), cfg, "http://tiles.invalid", client)
	defer s.close()
	key := tileTestKey()
	first := s.get(context.Background(), key, time.Now().Add(time.Minute))
	first.releaseBody()
	first.body = nil
	hit := s.get(context.Background(), key, time.Now().Add(time.Minute))
	shared := s.get(context.Background(), key, time.Now().Add(time.Minute))
	defer hit.releaseBody()
	defer shared.releaseBody()
	if calls != 1 || &hit.body[0] != &shared.body[0] {
		t.Fatal("cache hits allocated a second response body")
	}
	s.forget(key)
	for range 8 {
		other := key
		other.x = calls + 1
		result := s.get(context.Background(), other, time.Now().Add(time.Minute))
		if result.status != 200 {
			t.Fatalf("uncached delivery failed: %d", result.status)
		}
		s.mu.Lock()
		bounded := s.cacheBytes == cfg.CacheBytes && s.lru.Len() == 1 && s.occupied == 1 && s.cache[key] != nil
		s.mu.Unlock()
		result.releaseBody()
		if !bounded {
			t.Fatal("pinned cache bytes escaped accounting during replacement pressure")
		}
	}
	hit.releaseBody()
	s.mu.Lock()
	retained := s.cacheBytes == cfg.CacheBytes
	s.mu.Unlock()
	if !retained {
		t.Fatal("cache pin ended before its last delivery")
	}
	shared.releaseBody()
	s.mu.Lock()
	released := s.cacheBytes == 0 && s.lru.Len() == 0 && s.occupied == 0
	s.mu.Unlock()
	if !released {
		t.Fatal("expired cache body was not released with its final lease")
	}
}

func TestTileDeliverySafetyCeilingAndQueuedCancellation(t *testing.T) {
	cfg := (config.MapTiles{}).WithDefaults()
	cfg.Concurrency, cfg.CacheBytes = 16, 0
	s, calls := tileTestService(t, cfg, false)
	key := tileTestKey()
	var leases []mapTileResult
	defer func() {
		for _, result := range leases {
			result.releaseBody()
		}
	}()
	for range maximumTileBodyJobs {
		key.x++
		pending := tileTestLease(s, context.Background(), key)
		call := tileTestReceive(t, calls)
		call.reply <- "retained"
		leases = append(leases, tileTestReceive(t, pending))
	}
	ctx, cancel := context.WithCancel(context.Background())
	key.x++
	queued := tileTestLease(s, ctx, key)
	tileTestState(t, s, func(s *mapTileService) bool {
		return s.active == 0 && s.occupied == maximumTileBodyJobs && s.queue.Len() == 1
	})
	cancel()
	if result := tileTestReceive(t, queued); result.status != 503 {
		t.Fatal("queued cancellation must remain responsive behind slow deliveries")
	}
	select {
	case <-calls:
		t.Fatal("configured concurrency bypassed the body-memory ceiling")
	default:
	}
	for _, result := range leases {
		result.releaseBody()
	}
	tileTestState(t, s, func(s *mapTileService) bool { return s.occupied == 0 && s.queue.Len() == 0 })
}

func TestTileCacheLeaseReleaseAfterShutdown(t *testing.T) {
	s, calls := tileTestService(t, config.MapTiles{}, false)
	key := tileTestKey()
	pending := tileTestLease(s, context.Background(), key)
	call := tileTestReceive(t, calls)
	call.reply <- "cache"
	result := tileTestReceive(t, pending)
	result.releaseBody()
	hit := s.get(context.Background(), key, time.Now().Add(time.Minute))
	s.close()
	hit.releaseBody()
	hit.releaseBody()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheBytes != 0 || s.lru.Len() != 0 || s.occupied != 0 {
		t.Fatal("shutdown plus late cache lease release corrupted accounting")
	}
}
