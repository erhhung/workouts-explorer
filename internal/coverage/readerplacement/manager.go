// Package readerplacement coordinates fresh, pinned OSM reader connections.
// Strict placement requires a direct all-node service, not a proxy that can
// switch physical backends. Non-strict placement only admits logical endpoints.
// Single Kubernetes service hosts require SRV; explicit/generic hosts use one
// hostname identity across all addresses and do not claim per-node balancing.
package readerplacement

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const connectTimeout = 5 * time.Second
const cacheLifetime = 10 * time.Second

// ErrCleanupUnconfirmed means a failed Begin still owns an unconfirmed socket.
// Callers must retain the global reservation until cleanup is confirmed or its
// lease expires. The manager retains its local budget and retries on Close.
var ErrCleanupUnconfirmed = errors.New("OSM reader connection cleanup is unconfirmed")

// ReserveFunc atomically selects a globally least-active candidate. Keys are
// opaque SHA-256 hex strings; an empty result means capacity is unavailable.
type ReserveFunc func(context.Context, string, []string) (string, error)

// ClearFunc clears this caller's reservation after a failed Begin, never after
// success. The successful caller must Close its snapshot before releasing its slot.
type ClearFunc func(context.Context) error

type Manager struct {
	config         *pgx.ConnConfig
	requireStandby bool
	clusterKey     string
	lookupSRV      func(context.Context, string, string, string) (string, []*net.SRV, error)
	budget         chan struct{}
	probes         chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	active         map[*jobConnection]struct{}
	cache          []probeResult
	cachedAt       time.Time
	refreshing     chan struct{}
}

// New clones the normal reader/control pool configuration without acquiring it.
// The dedicated job budget and the two discovery probes are separate from that
// pool; there are no per-node pools or retained idle job connections.
func New(pool *pgxpool.Pool, requireStandby bool, maxActive int) (*Manager, error) {
	if pool == nil || maxActive < 1 {
		return nil, errors.New("invalid OSM reader placement configuration")
	}
	config := pool.Config().ConnConfig.Copy()
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["default_transaction_read_only"] = "on"
	config.Tracer = &safeTracer{query: config.Tracer}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		config: config, requireStandby: requireStandby, clusterKey: clusterIdentity(config),
		lookupSRV: net.DefaultResolver.LookupSRV, budget: make(chan struct{}, maxActive),
		probes: make(chan struct{}, 2), ctx: ctx, cancel: cancel,
		active: make(map[*jobConnection]struct{}),
	}, nil
}

// Close disables future begins, cancels discovery/connection work, and closes
// held job connections. It never clears caller-owned global reservations.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.mu.Lock()
	jobs := make([]*jobConnection, 0, len(m.active))
	for job := range m.active {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Go(func() { job.abort() })
	}
	wg.Wait()
}

// Begin discovers outside the reservation callback, then connects exclusively
// to its chosen endpoint. Failed attempts are returned to the caller for retry
// with the same job; success does not register a global-clear close callback.
func (m *Manager) Begin(ctx context.Context, targets []osm.MatcherGeneration, reserve ReserveFunc, clear ClearFunc) (*osm.MatcherSnapshot, error) {
	if m == nil || reserve == nil || clear == nil {
		return nil, errors.New("invalid OSM reader placement callbacks")
	}
	if err := validateTargets(targets); err != nil {
		return nil, err
	}
	if m.ctx.Err() != nil {
		return nil, context.Canceled
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	defer cancel()
	select {
	case m.budget <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	job := &jobConnection{manager: m}
	m.mu.Lock()
	m.active[job] = struct{}{}
	m.mu.Unlock()
	owned := false
	defer func() {
		if !owned {
			job.close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results, err := m.discover(ctx)
	if err != nil {
		return nil, err
	}
	eligible := make(map[string]endpoint)
	var keys []string
	for _, result := range results {
		ready := true
		for _, target := range targets {
			if result.heads[target.RegionID] < target.GenerationID {
				ready = false
				break
			}
		}
		if ready {
			eligible[result.endpoint.key] = result.endpoint
			keys = append(keys, result.endpoint.key)
		}
	}
	if len(keys) == 0 {
		return nil, osm.ErrMatcherReplicaNotReady
	}
	sort.Strings(keys)
	chosen, err := reserve(ctx, m.clusterKey, keys)
	// A callback may have committed its reservation before reporting an error.
	if err != nil {
		if !job.close() {
			return nil, errors.Join(ErrCleanupUnconfirmed, reservationError(err))
		}
		if clearErr := clearReservation(ctx, clear); clearErr != nil {
			return nil, clearErr
		}
		return nil, reservationError(err)
	}
	if chosen == "" {
		return nil, osm.ErrMatcherReplicaNotReady
	}
	endpoint, found := eligible[chosen]
	if !found {
		if !job.close() {
			return nil, ErrCleanupUnconfirmed
		}
		if err := clearReservation(ctx, clear); err != nil {
			return nil, err
		}
		return nil, errors.New("invalid OSM reader reservation result")
	}
	connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
	conn, err := m.connect(connectCtx, endpoint, job)
	connectCancel()
	if err == nil {
		job.mu.Lock()
		closing := job.closing
		job.conn = conn
		job.mu.Unlock()
		if closing {
			err = context.Canceled
		}
		var snapshot *osm.MatcherSnapshot
		if err == nil {
			snapshot, err = osm.BeginMatcherSnapshotOnConnection(ctx, conn, targets, m.requireStandby)
		}
		if err == nil && ctx.Err() == nil {
			job.arm()
			owned = true
			return snapshot, nil
		}
		if err == nil {
			err = ctx.Err()
		}
	}
	m.mu.Lock()
	m.cachedAt = time.Time{}
	m.mu.Unlock()
	if !job.close() {
		return nil, errors.Join(ErrCleanupUnconfirmed, sanitize(err))
	}
	if clearErr := clearReservation(ctx, clear); clearErr != nil {
		return nil, clearErr
	}
	return nil, sanitize(err)
}

func clearReservation(ctx context.Context, clear ClearFunc) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return reservationError(clear(cleanup))
}

func reservationError(err error) error {
	if err == nil {
		return nil
	}
	if safe := sanitize(err); !errors.Is(safe, osm.ErrMatcherReplicaNotReady) {
		return safe
	}
	return errors.New("OSM reader reservation operation failed")
}

func validateTargets(targets []osm.MatcherGeneration) error {
	if len(targets) == 0 || len(targets) > 256 {
		return &osm.MatcherBoundsError{Field: "target generations", Limit: 256}
	}
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		if target.RegionID == "" || target.GenerationID <= 0 || seen[target.RegionID] {
			return &osm.MatcherBoundsError{Field: "target generation provenance", Limit: 256}
		}
		seen[target.RegionID] = true
	}
	return nil
}

type jobConnection struct {
	manager    *Manager
	probe      bool
	mu         sync.Mutex
	conn       *pgx.Conn
	sockets    []*trackedSocket
	closing    bool
	releasable bool
	once       sync.Once
}

func (j *jobConnection) release() {
	j.once.Do(func() {
		j.manager.mu.Lock()
		delete(j.manager.active, j)
		j.manager.mu.Unlock()
		if j.probe {
			<-j.manager.probes
		} else {
			<-j.manager.budget
		}
	})
}

func (j *jobConnection) arm() {
	j.mu.Lock()
	j.releasable = true
	j.mu.Unlock()
	j.releaseIfClosed()
}

func (j *jobConnection) releaseIfClosed() {
	j.mu.Lock()
	closed := j.releasable
	for _, socket := range j.sockets {
		closed = closed && socket.PhysicalConnectionClosed()
	}
	j.mu.Unlock()
	if closed {
		j.release()
	}
}

func (j *jobConnection) abort() {
	// Closing sockets is safe while pgx is executing a query; pgx.Conn methods
	// themselves are not safe to call concurrently with an in-flight Begin.
	j.mu.Lock()
	j.closing = true
	sockets := append([]*trackedSocket(nil), j.sockets...)
	j.mu.Unlock()
	for _, socket := range sockets {
		_ = socket.Close()
	}
	j.releaseIfClosed()
}

func (j *jobConnection) close() bool {
	j.mu.Lock()
	j.closing = true
	j.releasable = true
	conn := j.conn
	sockets := append([]*trackedSocket(nil), j.sockets...)
	j.mu.Unlock()
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = conn.Close(ctx)
		cancel()
	}
	closed := true
	for _, socket := range sockets {
		_ = socket.Close()
		closed = closed && socket.PhysicalConnectionClosed()
	}
	if closed {
		j.release()
	}
	return closed
}

type trackedSocket struct {
	net.Conn
	mu      sync.Mutex
	closed  bool
	onClose func()
}

func (s *trackedSocket) PhysicalConnectionClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *trackedSocket) Close() error {
	err := s.Conn.Close()
	s.mu.Lock()
	s.closed = s.closed || err == nil || errors.Is(err, net.ErrClosed)
	callback := s.onClose
	closed := s.closed
	s.mu.Unlock()
	if closed && callback != nil {
		callback()
	}
	return err
}

func (m *Manager) connect(ctx context.Context, endpoint endpoint, job *jobConnection) (*pgx.Conn, error) {
	var last error = osm.ErrMatcherReplicaNotReady
	for _, address := range endpoint.addresses {
		for _, policy := range endpoint.policies {
			config := m.config.Copy()
			config.Host, config.Port, config.TLSConfig = address, endpoint.port, policy
			config.Fallbacks = nil // Never fail over to a different reserved backend.
			config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{address}, nil }
			config.ConnectTimeout = connectTimeout
			validate := config.ValidateConnect
			config.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
				if m.requireStandby {
					standby, err := checkRole(ctx, conn)
					if err != nil {
						return err
					}
					if !standby {
						return osm.ErrMatcherReplicaNotReady
					}
				}
				if validate != nil {
					return validate(ctx, conn)
				}
				return nil
			}
			if job != nil {
				dial := config.DialFunc
				config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
					conn, err := dial(ctx, network, address)
					if err != nil {
						return nil, err
					}
					socket := &trackedSocket{Conn: conn, onClose: job.releaseIfClosed}
					job.mu.Lock()
					job.sockets = append(job.sockets, socket)
					if job.closing {
						job.mu.Unlock()
						_ = socket.Close()
						return nil, context.Canceled
					}
					job.mu.Unlock()
					return socket, nil
				}
			}
			conn, err := pgx.ConnectConfig(ctx, config)
			if err == nil {
				standby, roleErr := checkRole(ctx, conn.PgConn())
				if roleErr == nil && (!m.requireStandby || standby) {
					return conn, nil
				}
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				_ = conn.Close(cleanup)
				cancel()
				err = roleErr
				if err == nil {
					err = osm.ErrMatcherReplicaNotReady
				}
			}
			last = sanitize(err)
			if job != nil {
				job.mu.Lock()
				sockets := append([]*trackedSocket(nil), job.sockets...)
				job.mu.Unlock()
				for _, socket := range sockets {
					_ = socket.Close()
					if !socket.PhysicalConnectionClosed() {
						return nil, errors.Join(ErrCleanupUnconfirmed, last)
					}
				}
			}
			if permanent(last) || ctx.Err() != nil {
				return nil, last
			}
		}
	}
	return nil, last
}

func checkRole(ctx context.Context, conn *pgconn.PgConn) (bool, error) {
	result := conn.ExecParams(ctx, "SELECT pg_catalog.pg_is_in_recovery()", nil, nil, nil, nil).Read()
	if result.Err != nil {
		return false, sanitize(result.Err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		return false, errors.New("invalid OSM reader role result")
	}
	switch string(result.Rows[0][0]) {
	case "t":
		return true, nil
	case "f":
		return false, nil
	default:
		return false, errors.New("invalid OSM reader role result")
	}
}
