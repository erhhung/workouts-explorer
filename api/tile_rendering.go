package api

import (
	"container/list"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/google/uuid"
)

const (
	maximumTileRequests = 256
	maximumTileWaiters  = 64
	// Each occupied job owns at most a 5 MiB body, or a bounded growing read
	// buffer. Cap this independently of the configurable upstream ceiling.
	maximumTileBodyJobs = 6
	tileUpstreamTimeout = 5 * time.Second
)

type mapTileKey struct {
	account, session, selection uuid.UUID
	generation                  int64
	coverage                    bool
	z, x, y                     int
}

func (k mapTileKey) source() string {
	if k.coverage {
		return "app.coverage_mvt"
	}
	return "app.raw_route_mvt"
}

func (k mapTileKey) kind() string {
	if k.coverage {
		return "coverage"
	}
	return "route"
}

type mapTileResult struct {
	body           []byte
	status         int
	outcome        string
	forwardStarted time.Time
	release        func()
}

func (r mapTileResult) releaseBody() {
	if r.release != nil {
		r.release()
	}
}

func tileUnavailable(outcome string) mapTileResult {
	return mapTileResult{status: http.StatusServiceUnavailable, outcome: outcome}
}

type mapTileEntry struct {
	key     mapTileKey
	body    []byte
	expires time.Time
	readers int
}

type mapTileJob struct {
	key          mapTileKey
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	result       mapTileResult
	waiters      int
	watchers     map[*mapTileWaiter]struct{}
	expires      time.Time
	enqueued     time.Time
	queued       *list.Element
	queueTimer   *time.Timer
	stopDeadline func() bool
	finished     bool
	running      bool
	occupied     bool
}

type mapTileWaiter struct{ ctx context.Context }

type mapTileService struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	config     config.MapTiles
	base       string
	client     *http.Client
	closed     bool
	active     int
	occupied   int
	jobs       map[mapTileKey]*mapTileJob
	queue      list.List
	cache      map[mapTileKey]*list.Element
	lru        list.List
	cacheBytes int
	requests   chan struct{}
	metrics    mapTileMetrics
}

func newMapTileClient(concurrency int) *http.Client {
	return &http.Client{
		Timeout: tileUpstreamTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Proxy: nil, DisableCompression: true,
			DialContext:     (&net.Dialer{Timeout: tileUpstreamTimeout, KeepAlive: 30 * time.Second}).DialContext,
			MaxConnsPerHost: concurrency, MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency,
			IdleConnTimeout: 60 * time.Second, ResponseHeaderTimeout: tileUpstreamTimeout,
			TLSHandshakeTimeout: tileUpstreamTimeout, MaxResponseHeaderBytes: 32 << 10,
		},
	}
}

func newMapTileService(ctx context.Context, cfg config.MapTiles, base string, client *http.Client) *mapTileService {
	cfg = cfg.WithDefaults()
	if client == nil {
		client = newMapTileClient(cfg.Concurrency)
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	s := &mapTileService{ctx: serviceCtx, cancel: cancel, config: cfg, base: base, client: client,
		jobs: make(map[mapTileKey]*mapTileJob), cache: make(map[mapTileKey]*list.Element),
		requests: make(chan struct{}, maximumTileRequests), metrics: newMapTileMetrics()}
	context.AfterFunc(ctx, s.close)
	return s
}

func (s *mapTileService) admit() bool {
	select {
	case s.requests <- struct{}{}:
		if s.ctx.Err() == nil {
			return true
		}
		<-s.requests
	default:
	}
	return false
}

func (s *mapTileService) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	for _, job := range s.jobs {
		s.finishLocked(job, tileUnavailable("shutdown"))
	}
	s.cache = make(map[mapTileKey]*list.Element)
	s.lru.Init()
	s.cacheBytes = 0
	s.mu.Unlock()
	s.client.CloseIdleConnections()
}

func (s *mapTileService) evictLocked(element *list.Element) bool {
	entry := element.Value.(*mapTileEntry)
	if entry.readers != 0 {
		return false
	}
	s.cacheBytes -= cap(entry.body)
	delete(s.cache, entry.key)
	s.lru.Remove(element)
	return true
}

func (s *mapTileService) forget(key mapTileKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if element := s.cache[key]; element != nil {
		element.Value.(*mapTileEntry).expires = time.Time{}
		s.evictLocked(element)
	}
}

func (s *mapTileService) cacheLocked(job *mapTileJob, result mapTileResult) {
	if s.config.CacheBytes == 0 || cap(result.body) > s.config.CacheBytes || len(result.body) > maximumTileBytes {
		return
	}
	now := time.Now()
	expires := now.Add(s.config.CacheTTL)
	if job.expires.Before(expires) {
		expires = job.expires
	}
	if !expires.After(now) {
		return
	}
	for element := s.lru.Back(); element != nil; {
		previous := element.Prev()
		if !element.Value.(*mapTileEntry).expires.After(now) {
			s.evictLocked(element)
		}
		element = previous
	}
	if old := s.cache[job.key]; old != nil {
		if !s.evictLocked(old) {
			return
		}
	}
	for s.lru.Len() >= s.config.CacheEntries || s.cacheBytes+cap(result.body) > s.config.CacheBytes {
		victim := s.lru.Back()
		for victim != nil && victim.Value.(*mapTileEntry).readers != 0 {
			victim = victim.Prev()
		}
		if victim == nil {
			return
		}
		s.evictLocked(victim)
	}
	s.cache[job.key] = s.lru.PushFront(&mapTileEntry{key: job.key, body: result.body, expires: expires})
	s.cacheBytes += cap(result.body)
}

// Publication detaches only this identity. Its slot outlives publication until
// the transport and every delivery have finished, including canceled writers.
func (s *mapTileService) finishLocked(job *mapTileJob, result mapTileResult) {
	if job.finished {
		return
	}
	job.finished = true
	job.result = result
	job.cancel()
	if job.stopDeadline != nil {
		job.stopDeadline()
	}
	if job.queueTimer != nil {
		job.queueTimer.Stop()
	}
	if job.queued != nil {
		s.metrics.queueWait.Record(s.ctx, time.Since(job.enqueued).Seconds())
		s.queue.Remove(job.queued)
		job.queued = nil
		s.metrics.queued.Add(s.ctx, -1)
	}
	if s.jobs[job.key] == job {
		delete(s.jobs, job.key)
	}
	close(job.done)
}

func (s *mapTileService) releaseSlotLocked(job *mapTileJob) {
	if !job.occupied || job.running || job.waiters != 0 {
		return
	}
	job.occupied = false
	job.result.body = nil
	s.occupied--
	s.metrics.occupied.Add(s.ctx, -1)
	for !s.closed && s.occupied < min(s.config.Concurrency, maximumTileBodyJobs) && s.queue.Len() > 0 {
		next := s.queue.Front().Value.(*mapTileJob)
		if next.ctx.Err() != nil || time.Since(next.enqueued) >= s.config.QueueTimeout {
			s.finishLocked(next, tileUnavailable("queue_timeout"))
			continue
		}
		s.startLocked(next)
	}
}

func (s *mapTileService) startLocked(job *mapTileJob) {
	if job.queued != nil {
		s.queue.Remove(job.queued)
		job.queued = nil
		s.metrics.queued.Add(s.ctx, -1)
	}
	if job.queueTimer != nil {
		job.queueTimer.Stop()
	}
	s.metrics.queueWait.Record(s.ctx, time.Since(job.enqueued).Seconds())
	s.active++
	s.occupied++
	s.metrics.occupied.Add(s.ctx, 1)
	job.running, job.occupied = true, true
	s.metrics.active.Add(s.ctx, 1)
	go func() {
		result := s.fetch(job.ctx, job.key)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.active--
		job.running = false
		s.metrics.active.Add(s.ctx, -1)
		if !job.finished {
			if job.ctx.Err() != nil {
				result = tileUnavailable("deadline")
			} else if result.status == http.StatusOK && !s.closed {
				// A caller's cancellation can precede its goroutine detaching.
				for watcher := range job.watchers {
					if watcher.ctx.Err() == nil {
						s.cacheLocked(job, result)
						break
					}
				}
			}
			s.finishLocked(job, result)
		}
		s.releaseSlotLocked(job)
	}()
}

func (s *mapTileService) get(ctx context.Context, key mapTileKey, expires time.Time) (result mapTileResult) {
	defer func() { s.metrics.event(ctx, key, result.outcome) }()
	s.mu.Lock()
	if ctx.Err() != nil || s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return tileUnavailable("canceled")
	}
	if element := s.cache[key]; element != nil {
		entry := element.Value.(*mapTileEntry)
		if entry.expires.After(time.Now()) {
			entry.readers++
			release := sync.OnceFunc(func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				entry.readers--
				if s.cache[key] == element && !entry.expires.After(time.Now()) {
					s.evictLocked(element)
				}
			})
			s.lru.MoveToFront(element)
			s.mu.Unlock()
			s.metrics.event(ctx, key, "cache_hit")
			return mapTileResult{body: entry.body, status: http.StatusOK, outcome: "success", release: release}
		}
		s.evictLocked(element)
	}
	job := s.jobs[key]
	if job != nil && job.ctx.Err() != nil {
		s.finishLocked(job, tileUnavailable("deadline"))
		job = nil
	}
	event := "coalesced"
	if job != nil && job.waiters >= maximumTileWaiters {
		s.mu.Unlock()
		return tileUnavailable("waiter_limit")
	}
	if job == nil {
		if s.occupied >= min(s.config.Concurrency, maximumTileBodyJobs) && s.queue.Len() >= s.config.QueueSize {
			s.mu.Unlock()
			return tileUnavailable("queue_full")
		}
		event = "cache_miss"
		jobCtx, cancel := context.WithTimeout(s.ctx, s.config.RequestTimeout)
		job = &mapTileJob{key: key, ctx: jobCtx, cancel: cancel, done: make(chan struct{}),
			expires: expires, enqueued: time.Now(), watchers: make(map[*mapTileWaiter]struct{})}
		s.jobs[key] = job
		job.stopDeadline = context.AfterFunc(jobCtx, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.finishLocked(job, tileUnavailable("deadline"))
		})
		if s.occupied < min(s.config.Concurrency, maximumTileBodyJobs) {
			s.startLocked(job)
		} else {
			job.queued = s.queue.PushBack(job)
			s.metrics.queued.Add(s.ctx, 1)
			job.queueTimer = time.AfterFunc(s.config.QueueTimeout, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if job.queued != nil {
					s.finishLocked(job, tileUnavailable("queue_timeout"))
				}
			})
		}
	}
	job.waiters++
	watcher := &mapTileWaiter{ctx: ctx}
	job.watchers[watcher] = struct{}{}
	if expires.Before(job.expires) {
		job.expires = expires
	}
	s.mu.Unlock()
	s.metrics.event(ctx, key, event)
	release := sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		job.waiters--
		delete(job.watchers, watcher)
		if job.waiters == 0 && !job.finished {
			s.finishLocked(job, tileUnavailable("abandoned"))
		}
		s.releaseSlotLocked(job)
	})
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()
	select {
	case <-ctx.Done():
		return tileUnavailable("canceled")
	case <-job.done:
		if ctx.Err() != nil {
			return tileUnavailable("canceled")
		}
		result = job.result
		if result.status == http.StatusOK {
			result.release = release
			transferred = true
		}
		return result
	}
}

func (s *mapTileService) fetch(ctx context.Context, key mapTileKey) mapTileResult {
	result := mapTileResult{status: http.StatusBadGateway, outcome: "upstream_error", forwardStarted: time.Now()}
	upstream, err := mapTileFunctionUpstreamURL(s.base, key.source(), key.selection, key.account, key.session, key.generation, key.z, key.x, key.y)
	if err != nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, tileUpstreamTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return result
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := s.client.Do(request)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	encodings := response.Header.Values("Content-Encoding")
	if !validMVTResponse(response.StatusCode, response.Header.Get("Content-Type")) || response.Uncompressed ||
		len(encodings) > 1 || len(encodings) == 1 && strings.ToLower(strings.TrimSpace(encodings[0])) != "identity" && strings.TrimSpace(encodings[0]) != "" {
		result.outcome = "invalid_response"
		return result
	}
	// Jump from 2 MiB straight to the final 5 MiB+1 cap. The largest growth
	// overlap is then 7 MiB+1, with no full-size compaction copy afterward.
	body := make([]byte, 0, 512)
	emptyReads := 0
	for {
		if len(body) == cap(body) {
			capacity := 2 * cap(body)
			if capacity >= maximumTileBytes/2 {
				capacity = maximumTileBytes + 1
			}
			grown := make([]byte, len(body), capacity)
			copy(grown, body)
			body = grown
		}
		n, readErr := response.Body.Read(body[len(body):cap(body)])
		body = body[:len(body)+n]
		if len(body) > maximumTileBytes {
			result.outcome = "too_large"
			return result
		}
		if n == 0 && readErr == nil {
			emptyReads++
		} else {
			emptyReads = 0
		}
		if ctx.Err() != nil || emptyReads >= 100 || readErr != nil && readErr != io.EOF {
			result.outcome = "body_error"
			return result
		}
		if readErr != nil {
			break
		}
	}
	if response.StatusCode == http.StatusNoContent && len(body) != 0 {
		result.outcome = "invalid_response"
		return result
	}
	if len(body) == 0 {
		body = nil
	} else if len(body) <= 4096 && len(body) != cap(body) {
		compact := make([]byte, len(body))
		copy(compact, body)
		body = compact
	}
	result.body, result.status, result.outcome = body, http.StatusOK, "success"
	return result
}
